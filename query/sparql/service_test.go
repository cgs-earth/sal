package sparql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFederatedPlanExtractsServiceAndBuildsRemoteQuery(t *testing.T) {
	plan, err := federatedPlanFor(`
PREFIX schema: <https://schema.org/>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

SELECT ?s ?label
WHERE {
  ?s schema:sameAs ?remote .
  SERVICE <https://example.org/sparql> {
    ?remote rdfs:label ?label .
  }
}`, SimpleObjects)

	require.NoError(t, err)
	require.Equal(t, []string{"s", "label"}, plan.Projection)
	require.Equal(t, []string{"s", "remote"}, plan.LocalVars)
	require.Len(t, plan.Services, 1)
	require.Equal(t, "https://example.org/sparql", plan.Services[0].Endpoint)
	require.Equal(t, []string{"remote", "label"}, plan.Services[0].Vars)
	require.Contains(t, plan.Services[0].Query, "PREFIX schema:")
	require.Contains(t, plan.Services[0].Query, "SELECT DISTINCT ?remote ?label WHERE")
	require.Contains(t, plan.LocalSQL, `t0.object AS "remote"`)
}

func TestFederatedPlanPushesLimitIntoIndependentServiceBlock(t *testing.T) {
	plan, err := federatedPlanFor(`
PREFIX schema: <https://schema.org/>

SELECT ?s
WHERE {
  ?localSubject ?localPredicate ?localObject .

  SERVICE <https://graph.geoconnex.us/> {
    ?s schema:name "Potassium" .
  }
}
LIMIT 10`, SimpleObjects)

	require.NoError(t, err)
	require.Len(t, plan.Services, 1)
	require.Contains(t, plan.Services[0].Query, `?s schema:name "Potassium" .`)
	require.Contains(t, plan.Services[0].Query, "LIMIT 10")
	require.Contains(t, plan.LocalSQL, "LIMIT 1")
}

func TestFederatedPlanDoesNotPushLimitIntoJoinedServiceBlock(t *testing.T) {
	plan, err := federatedPlanFor(`
PREFIX schema: <https://schema.org/>

SELECT ?s ?name
WHERE {
  ?s schema:sameAs ?remote .

  SERVICE <https://graph.geoconnex.us/> {
    ?remote schema:name ?name .
  }
}
LIMIT 10`, SimpleObjects)

	require.NoError(t, err)
	require.Len(t, plan.Services, 1)
	require.NotContains(t, plan.Services[0].Query, "LIMIT 10")
	require.NotContains(t, plan.LocalSQL, "LIMIT 1")
}

func TestFederatedSQLJoinsServiceRowsAgainstLocalRows(t *testing.T) {
	plan := &federatedPlan{
		LocalSQL:   `SELECT t0.subject AS "s", t0.object AS "remote" FROM triples AS t0`,
		LocalVars:  []string{"s", "remote"},
		Projection: []string{"s", "label"},
		Services: []serviceBlock{{
			Vars: []string{"remote", "label"},
		}},
		Limit: -1,
	}

	sql := federatedSQL(plan, [][][]string{{{"https://example.org/alice", "Alice"}}}, 100)

	require.Contains(t, sql, `service_0("remote", "label") AS (VALUES ('https://example.org/alice', 'Alice'))`)
	require.Contains(t, sql, `INNER JOIN service_0 AS s0 ON l."remote" = s0."remote"`)
	require.Contains(t, sql, `SELECT l."s" AS "s", s0."label" AS "label"`)
	require.Contains(t, sql, "LIMIT 100")
}

func TestFetchServiceRowsPostsQueryAndParsesSPARQLJSON(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, r.ParseForm())
		gotQuery = r.Form.Get("query")
		w.Header().Set("Content-Type", sparqlResultsJSON)
		_, err := w.Write([]byte(`{
		  "head": {"vars": ["remote", "label"]},
		  "results": {"bindings": [
		    {
		      "remote": {"type": "uri", "value": "https://example.org/alice"},
		      "label": {"type": "literal", "value": "Alice"}
		    }
		  ]}
		}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	rows, err := fetchServiceRows(context.Background(), server.Client(), serviceBlock{
		Endpoint: server.URL,
		Vars:     []string{"remote", "label"},
		Query:    "SELECT ?remote ?label WHERE { ?remote ?p ?label }",
	})

	require.NoError(t, err)
	require.Equal(t, "SELECT ?remote ?label WHERE { ?remote ?p ?label }", gotQuery)
	require.Equal(t, [][]string{{"https://example.org/alice", "Alice"}}, rows)
}

func TestDuckDBCommandDoesNotPutStatementInArgv(t *testing.T) {
	statement := strings.Repeat("SELECT 'x';\n", 10000)
	cmd := duckDBCommand(context.Background())
	cmd.Stdin = strings.NewReader(statement)

	require.Equal(t, []string{"duckdb", "-csv"}, cmd.Args)
	require.NotContains(t, strings.Join(cmd.Args, " "), statement)
}

func TestFederatedPlanRejectsUnboundProjection(t *testing.T) {
	_, err := federatedPlanFor(`
PREFIX schema: <https://schema.org/>

SELECT ?missing
WHERE {
  ?s schema:sameAs ?remote .
  SERVICE <https://example.org/sparql> {
    ?remote schema:name ?label .
  }
}`, SimpleObjects)

	require.ErrorContains(t, err, "projected variable ?missing is not bound")
}
