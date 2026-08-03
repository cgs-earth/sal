package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/stretchr/testify/require"
)

type endpointRunner struct {
	result salsparql.Result
	err    error
	query  string
}

func (r *endpointRunner) Run(_ context.Context, query string) (salsparql.Result, error) {
	r.query = query
	return r.result, r.err
}

// endpointUIRunner is a UIRunner whose four query surfaces are all canned responses.
type endpointUIRunner struct {
	endpointRunner
	collection salsparql.FeatureCollection
	stats      salsparql.TableStats
	sqlResult  salsparql.Result
	sqlErr     error
	sql        string
	limit      int
	offset     int
}

func (r *endpointUIRunner) Geometries(_ context.Context, limit int, offset int) (salsparql.FeatureCollection, error) {
	r.limit = limit
	r.offset = offset
	return r.collection, r.err
}

func (r *endpointUIRunner) RunSQL(_ context.Context, sql string) (salsparql.Result, error) {
	r.sql = sql
	return r.sqlResult, r.sqlErr
}

func (r *endpointUIRunner) Stats(_ context.Context) (salsparql.TableStats, error) {
	return r.stats, r.err
}

func newUIServer(t *testing.T, runner *endpointUIRunner) *httptest.Server {
	t.Helper()
	handler, err := NewEndpointWithUI(runner)
	require.NoError(t, err)
	return httptest.NewServer(handler)
}

func TestEndpointAcceptsGETQueryAndReturnsSPARQLJSON(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{
		Header: []string{"s", "name"},
		Rows: [][]string{
			{"https://example.org/alice", "Alice"},
		},
	}}
	server := httptest.NewServer(NewEndpoint(runner))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sparql?query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", sparqlResultsJSON)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, sparqlResultsJSON, resp.Header.Get("Content-Type"))
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)

	var body sparqlJSONResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, []string{"s", "name"}, body.Head.Vars)
	require.Equal(t, "uri", body.Results.Bindings[0]["s"].Type)
	require.Equal(t, "https://example.org/alice", body.Results.Bindings[0]["s"].Value)
	require.Equal(t, "literal", body.Results.Bindings[0]["name"].Type)
	require.Equal(t, "Alice", body.Results.Bindings[0]["name"].Value)
}

func TestEndpointWithUIServesTheAppAtRoot(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `<div id="root">`)
	require.Contains(t, string(body), "/assets/")
}

func TestEndpointWithUIFallsBackToIndexForUnknownPaths(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp, err := http.Get(server.URL + "/does-not-exist")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
}

func TestEndpointWithUIRunsSQL(t *testing.T) {
	runner := &endpointUIRunner{sqlResult: salsparql.Result{
		Header: []string{"count"},
		Rows:   [][]string{{"42"}},
	}}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/sql", "application/json", strings.NewReader(`{"sql":"SELECT COUNT(*) FROM triples;  "}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "SELECT COUNT(*) FROM triples", runner.sql)

	var body salsparql.Result
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, []string{"count"}, body.Header)
	require.Equal(t, [][]string{{"42"}}, body.Rows)
	require.Equal(t, "1 rows", body.Message)
}

func TestEndpointWithUIReportsSQLErrorsAsJSON(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{sqlErr: fmt.Errorf("Catalog Error: Table with name nope does not exist")})
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/sql", "application/json", strings.NewReader(`{"sql":"SELECT * FROM nope"}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body["error"], "Table with name nope does not exist")
}

func TestEndpointWithUIRejectsEmptySQL(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/sql", "application/json", strings.NewReader(`{"sql":"   "}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestTruncateResultReportsTheFullRowCount(t *testing.T) {
	rows := make([][]string, maxUIRows+5)
	for i := range rows {
		rows[i] = []string{"row"}
	}

	truncated := truncateResult(salsparql.Result{Header: []string{"value"}, Rows: rows})

	require.Len(t, truncated.Rows, maxUIRows)
	require.Equal(t, fmt.Sprintf("%d rows (showing the first %d)", maxUIRows+5, maxUIRows), truncated.Message)
}

func TestEndpointWithUIReturnsTableStats(t *testing.T) {
	runner := &endpointUIRunner{stats: salsparql.TableStats{
		TablePath:  "/tmp/warehouse/sal/triples",
		Triples:    12,
		Subjects:   3,
		Predicates: 4,
		Objects:    9,
		Snapshots: salsparql.Result{
			Header: []string{"snapshot_id"},
			Rows:   [][]string{{"123"}},
		},
	}}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/stats")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body salsparql.TableStats
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, int64(12), body.Triples)
	require.Equal(t, int64(9), body.Objects)
	require.Equal(t, "/tmp/warehouse/sal/triples", body.TablePath)
	require.Equal(t, [][]string{{"123"}}, body.Snapshots.Rows)
}

func TestEndpointWithUIReturnsGeometryFeatureCollection(t *testing.T) {
	runner := &endpointUIRunner{collection: salsparql.FeatureCollection{
		Type: "FeatureCollection",
		Features: []salsparql.Feature{
			{
				Type:     "Feature",
				Geometry: json.RawMessage(`{"type":"Point","coordinates":[-77.0365,38.8977]}`),
				Properties: map[string]string{
					"subject": "https://example.org/place",
				},
			},
		},
	}}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Get(server.URL + "/geometries?limit=500&offset=12")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/geo+json", resp.Header.Get("Content-Type"))
	require.Equal(t, 100, runner.limit)
	require.Equal(t, 12, runner.offset)

	var body salsparql.FeatureCollection
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "FeatureCollection", body.Type)
	require.Len(t, body.Features, 1)
	require.JSONEq(t, `{"type":"Point","coordinates":[-77.0365,38.8977]}`, string(body.Features[0].Geometry))
	require.Equal(t, "https://example.org/place", body.Features[0].Properties["subject"])
}

func TestEndpointAcceptsFormPOSTQuery(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner))
	defer server.Close()

	resp, err := http.Post(
		server.URL+"/sparql",
		"application/x-www-form-urlencoded",
		strings.NewReader("query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D"),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)
}

func TestEndpointAcceptsSPARQLQueryPOSTBody(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/sparql", strings.NewReader("SELECT ?s WHERE { ?s ?p ?o }"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/sparql-query")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)
}

func TestEndpointRejectsUnsupportedAcceptHeader(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sparql?query=SELECT+%3Fs+WHERE+%7B%7D", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/turtle")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusNotAcceptable, resp.StatusCode)
}

func TestEndpointRejectsMissingQuery(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/sparql")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestEndpointRejectsUnsupportedPOSTMediaType(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/sparql", "application/json", strings.NewReader(`{"query":"SELECT ?s WHERE {}"}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
}

func TestEndpointReturnsBadRequestForSPARQLError(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{err: fmt.Errorf("parse SPARQL query")}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/sparql?query=ASK+%7B%7D")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
