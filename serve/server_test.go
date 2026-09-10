package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
)

type endpointRunner struct {
	result salsparql.Result
	err    error
	query  string
	// snapshots are the snapshot IDs HasSnapshot reports the table as having.
	snapshots   []int64
	snapshotErr error
	// snapshot is the snapshot AtSnapshot was last asked for, zero when a query
	// went to the current table.
	snapshot int64
	// reasoning is what the last query asked for.
	reasoning bool
}

func (r *endpointRunner) Run(_ context.Context, query string, reasoning bool) (salsparql.Result, error) {
	r.query = query
	r.reasoning = reasoning
	return r.result, r.err
}

func (r *endpointRunner) HasSnapshot(_ context.Context, snapshotID int64) (bool, error) {
	return slices.Contains(r.snapshots, snapshotID), r.snapshotErr
}

func (r *endpointRunner) AtSnapshot(snapshotID int64) salsparql.Runner {
	r.snapshot = snapshotID
	return r
}

// endpointUIRunner is a UIRunner whose four query surfaces are all canned responses.
type endpointUIRunner struct {
	endpointRunner
	collection salsparql.FeatureCollection
	extent     salsparql.Feature
	stats      salsparql.TableStats
	sqlResult  salsparql.Result
	sqlErr     error
	sql        string
	geometries salsparql.GeometryQuery
}

func (r *endpointUIRunner) Geometries(_ context.Context, query salsparql.GeometryQuery) (salsparql.FeatureCollection, error) {
	r.geometries = query
	return r.collection, r.err
}

func (r *endpointUIRunner) Extent(_ context.Context) (salsparql.Feature, error) {
	return r.extent, r.err
}

func (r *endpointUIRunner) RunSQL(_ context.Context, sql string) (salsparql.Result, error) {
	r.sql = sql
	return r.sqlResult, r.sqlErr
}

func (r *endpointUIRunner) Stats(_ context.Context) (salsparql.TableStats, error) {
	return r.stats, r.err
}

// Translate is the real translation, since it touches neither DuckDB nor the
// table and the endpoint's job is to report what that translation produces.
func (r *endpointUIRunner) Translate(query string, reasoning bool) (string, error) {
	return salsparql.DuckDBRunner{}.Translate(query, reasoning)
}

func newUIServer(t *testing.T, runner *endpointUIRunner) *httptest.Server {
	t.Helper()
	handler, err := NewEndpointWithUI(runner, "", "")
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
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
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

func TestSparqlBindingTypeRecognizesNonHTTPIRIs(t *testing.T) {
	require.Equal(t, "uri", sparqlBindingType("http://schema.org/name"))
	require.Equal(t, "uri", sparqlBindingType("https://example.org/alice"))
	require.Equal(t, "uri", sparqlBindingType("salmodule://github.com/cgs-earth/python-geoconnex/salmodule#Task"))
	require.Equal(t, "uri", sparqlBindingType("oci://ghcr.io/cgs-earth/sal:latest"))
	require.Equal(t, "uri", sparqlBindingType("urn:sha256:deadbeef"))
	require.Equal(t, "bnode", sparqlBindingType("_:b0"))
	require.Equal(t, "literal", sparqlBindingType("Alice"))
	require.Equal(t, "literal", sparqlBindingType("note: see http://example.org"))
	require.Equal(t, "literal", sparqlBindingType("12:30:00"))
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

// browserGet issues a GET carrying the headers a browser sends when it navigates to a
// URL, which is what tells a link to a UI tab apart from a call to the endpoint
// living at the same path.
func browserGet(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestEndpointWithUIServesTheAppForABrowserNavigationToSparql(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp := browserGet(t, server.URL+"/sparql")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `<div id="root">`)
}

func TestEndpointWithUIStillAnswersSPARQLRequestsAtSparql(t *testing.T) {
	runner := &endpointUIRunner{endpointRunner: endpointRunner{result: salsparql.Result{Header: []string{"s"}}}}
	server := newUIServer(t, runner)
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
}

// A browser opening a SPARQL Protocol URL by hand still means the endpoint: the
// query parameter says so no matter what asked for it.
func TestEndpointWithUIAnswersSPARQLProtocolGETEvenFromABrowser(t *testing.T) {
	runner := &endpointUIRunner{endpointRunner: endpointRunner{result: salsparql.Result{Header: []string{"s"}}}}
	server := newUIServer(t, runner)
	defer server.Close()

	resp := browserGet(t, server.URL+"/sparql?query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, sparqlResultsJSON, resp.Header.Get("Content-Type"))
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)
}

// The share link a tab copies carries its query in `q`, which is the UI's own
// parameter and must not divert the request to the endpoint.
func TestEndpointWithUIServesTheAppForASharedQueryLink(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp := browserGet(t, server.URL+"/sparql?q=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
}

func TestEndpointWithUIServesTheAppForABrowserNavigationToBlobs(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp := browserGet(t, server.URL+"/blobs")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
}

func TestEndpointWithUIStillServesBlobsToANonBrowser(t *testing.T) {
	dir := t.TempDir()
	body := []byte("@prefix ex: <https://example.org/> .")
	digest := writeBlob(t, dir, body)

	handler, err := NewEndpointWithUI(&endpointUIRunner{}, dir, "")
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/blobs/" + digest)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

// The UI's own Blobs tab downloads through fetch(), which a browser marks as a
// same-origin request rather than a navigation, so it must reach the endpoint.
func TestEndpointWithUIServesBlobsToTheUIsOwnFetch(t *testing.T) {
	dir := t.TempDir()
	body := []byte("some vocabulary bytes")
	digest := writeBlob(t, dir, body)

	handler, err := NewEndpointWithUI(&endpointUIRunner{}, dir, "")
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/blobs/"+digest, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Mode", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, got)
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

func TestEndpointWithUITranslatesSPARQLToSQLWithoutRunningIt(t *testing.T) {
	runner := &endpointUIRunner{}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/sparql/translate", "application/json",
		strings.NewReader(`{"query":"SELECT ?s WHERE { ?s ?p ?o } LIMIT 5"}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	var body struct {
		SQL string `json:"sql"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body.SQL, "SELECT")
	require.Contains(t, body.SQL, "FROM triples")
	require.Contains(t, body.SQL, "LIMIT 5")
	require.Empty(t, runner.sql, "translation must not run anything")
	require.Empty(t, runner.query, "translation must not run anything")
}

func TestEndpointWithUIReportsSPARQLTranslationErrorsAsJSON(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/sparql/translate", "application/json",
		strings.NewReader(`{"query":"ASK { ?s ?p ?o }"}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body["error"], "SELECT")
}

func TestEndpointWithUIRejectsEmptySPARQLTranslation(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/sparql/translate", "application/json", strings.NewReader(`{"query":"  "}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestEndpointWithUIOnlyTranslatesOnPOST(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/sparql/translate")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	require.Equal(t, "POST", resp.Header.Get("Allow"))
}

// newModuleServer serves the SAL module endpoint against a canned inspector so
// that the handler can be tested without cloning or building a real module.
func newModuleServer(t *testing.T, inspect ModuleInspector) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/api/salmodule", salmoduleHandler{inspect: inspect})
	return httptest.NewServer(mux)
}

func TestSalModuleEndpointReturnsTheOntologyDocument(t *testing.T) {
	var inspected string
	server := newModuleServer(t, func(_ context.Context, reference string) (*salmodule.ModuleOntology, error) {
		inspected = reference
		return &salmodule.ModuleOntology{
			Namespace: "salmodule://github.com/owner/repo/",
			Document:  []byte(`{"@context":{"@vocab":"salmodule://github.com/owner/repo/"}}`),
		}, nil
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/salmodule?module=" + url.QueryEscape("salmodule://github.com/owner/repo/"))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "salmodule://github.com/owner/repo/", inspected)

	var body struct {
		Module   string         `json:"module"`
		Ontology map[string]any `json:"ontology"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "salmodule://github.com/owner/repo/", body.Module)
	require.Contains(t, body.Ontology, "@context")
}

func TestSalModuleEndpointReportsInspectionFailuresAsJSON(t *testing.T) {
	server := newModuleServer(t, func(_ context.Context, _ string) (*salmodule.ModuleOntology, error) {
		return nil, fmt.Errorf("SAL module salmodule://github.com/owner/repo/ has no Dockerfile in its repository root")
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/salmodule?module=owner/repo")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body["error"], "has no Dockerfile")
}

func TestSalModuleEndpointRejectsAMissingModuleParameter(t *testing.T) {
	server := newModuleServer(t, func(_ context.Context, _ string) (*salmodule.ModuleOntology, error) {
		t.Fatal("a module should not be inspected without a reference")
		return nil, nil
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/salmodule?module=%20")
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
		Vocabularies: salsparql.Result{
			Header: []string{"vocabulary", "version", "format", "imported"},
			Rows: [][]string{
				{"https://schema.org/", "", "", "yes"},
				{"oci://ghcr.io/cgs-earth/sal:abc123", "", "", "yes"},
			},
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
	require.Equal(t, [][]string{
		{"https://schema.org/", "", "", "yes"},
		{"oci://ghcr.io/cgs-earth/sal:abc123", "", "", "yes"},
	}, body.Vocabularies.Rows)
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

	resp, err := http.Get(server.URL + "/geometries?limit=5000&offset=12")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/geo+json", resp.Header.Get("Content-Type"))
	require.Equal(t, salsparql.GeometryQuery{Limit: salsparql.MaxGeometries, Offset: 12}, runner.geometries)

	var body salsparql.FeatureCollection
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "FeatureCollection", body.Type)
	require.Len(t, body.Features, 1)
	require.JSONEq(t, `{"type":"Point","coordinates":[-77.0365,38.8977]}`, string(body.Features[0].Geometry))
	require.Equal(t, "https://example.org/place", body.Features[0].Properties["subject"])
}

func TestEndpointWithUIFiltersGeometriesByBoundingBox(t *testing.T) {
	runner := &endpointUIRunner{collection: salsparql.FeatureCollection{Type: "FeatureCollection"}}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Get(server.URL + "/geometries?bbox=-90.5,40,-89,41.25&limit=20")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, salsparql.GeometryQuery{
		Limit: 20,
		BBox:  &salsparql.BBox{MinX: -90.5, MinY: 40, MaxX: -89, MaxY: 41.25},
	}, runner.geometries)
}

func TestEndpointWithUIRejectsMalformedBoundingBox(t *testing.T) {
	runner := &endpointUIRunner{}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Get(server.URL + "/geometries?bbox=-90,40,-89")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "minX,minY,maxX,maxY")
	require.Nil(t, runner.geometries.BBox)
}

func TestEndpointWithUIReturnsTheGeometryExtent(t *testing.T) {
	runner := &endpointUIRunner{extent: salsparql.Feature{
		Type:       "Feature",
		BBox:       []float64{-90, 40, -89, 41},
		Geometry:   json.RawMessage(`{"type":"Polygon","coordinates":[[[-90,40],[-89,40],[-89,41],[-90,41],[-90,40]]]}`),
		Properties: map[string]string{"geometries": "6"},
	}}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Get(server.URL + "/geometries/extent")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/geo+json", resp.Header.Get("Content-Type"))
	var body salsparql.Feature
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, []float64{-90, 40, -89, 41}, body.BBox)
	require.Equal(t, "6", body.Properties["geometries"])
	require.JSONEq(t, `{"type":"Polygon","coordinates":[[[-90,40],[-89,40],[-89,41],[-90,41],[-90,40]]]}`, string(body.Geometry))
}

func TestSparqlJSONResultReportsWKTBindingsAsGeometryLiterals(t *testing.T) {
	response := sparqlJSONResult(salsparql.Result{
		Header: []string{"s", "wkt", "name"},
		Rows:   [][]string{{"https://example.org/place", "POINT (-89.5 40.5)", "Point of interest"}},
	})

	require.Len(t, response.Results.Bindings, 1)
	binding := response.Results.Bindings[0]
	require.Equal(t, sparqlJSONBinding{Type: "uri", Value: "https://example.org/place"}, binding["s"])
	require.Equal(t, sparqlJSONBinding{
		Type:     "literal",
		Value:    "POINT (-89.5 40.5)",
		Datatype: "http://www.opengis.net/ont/geosparql#wktLiteral",
	}, binding["wkt"])
	require.Equal(t, sparqlJSONBinding{Type: "literal", Value: "Point of interest"}, binding["name"])
}

func TestEndpointAcceptsFormPOSTQuery(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
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
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
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
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
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
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/sparql")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestEndpointRejectsUnsupportedPOSTMediaType(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	resp, err := http.Post(server.URL+"/sparql", "application/json", strings.NewReader(`{"query":"SELECT ?s WHERE {}"}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
}

func TestEndpointReturnsBadRequestForSPARQLError(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{err: fmt.Errorf("parse SPARQL query")}, "", ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/sparql?query=ASK+%7B%7D")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// writeBlob writes body to dir named by its SHA-256 digest, the shape
// PinnedVocabularies stores a pinned document under, and returns the digest.
func writeBlob(t *testing.T, dir string, body []byte) string {
	t.Helper()
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, digest), body, 0644))
	return digest
}

func TestBlobEndpointServesAPinnedDocumentByDigest(t *testing.T) {
	dir := t.TempDir()
	body := []byte("@prefix ex: <https://example.org/> .")
	digest := writeBlob(t, dir, body)

	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/blobs/" + digest)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestBlobEndpointStripsUrnSha256Prefix(t *testing.T) {
	dir := t.TempDir()
	body := []byte("some vocabulary bytes")
	digest := writeBlob(t, dir, body)

	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/blobs/urn:sha256:" + digest)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

// writeModuleBlob stores body the way a pinned salmodule:// vocabulary is
// stored: named by the git commit hash of the module rather than a digest.
func writeModuleBlob(t *testing.T, dir string, body []byte) string {
	t.Helper()
	commit := strings.Repeat("ab", 20)
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, commit), body, 0644))
	return commit
}

func TestBlobEndpointServesAModuleOntologyByCommitHash(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"@context": {"@vocab": "salmodule://example.org/module/"}}`)
	commit := writeModuleBlob(t, dir, body)

	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/blobs/" + commit)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestBlobEndpointStripsUrnGitCommitHashPrefix(t *testing.T) {
	dir := t.TempDir()
	body := []byte("module ontology bytes")
	commit := writeModuleBlob(t, dir, body)

	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/blobs/urn:git-commit-hash:" + commit)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestBlobEndpointReturnsNotFoundForUnknownDigest(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/blobs/" + strings.Repeat("0", 64))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestBlobEndpointReturnsNotFoundForMalformedDigest(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/blobs/not-a-valid-digest")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestBlobEndpointSupportsRangeRequests(t *testing.T) {
	dir := t.TempDir()
	body := []byte("0123456789abcdef")
	digest := writeBlob(t, dir, body)

	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/blobs/"+digest, nil)
	require.NoError(t, err)
	req.Header.Set("Range", "bytes=2-5")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	require.Equal(t, "bytes 2-5/16", resp.Header.Get("Content-Range"))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "2345", string(got))
}

func TestBlobEndpointRejectsUnsupportedMethod(t *testing.T) {
	dir := t.TempDir()
	digest := writeBlob(t, dir, []byte("body"))

	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, dir, ""))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/blobs/"+digest, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// snapshotGet queries the SPARQL endpoint at the given path as a SPARQL client would.
func snapshotGet(t *testing.T, serverURL string, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, serverURL+path+"?query="+url.QueryEscape("SELECT ?s WHERE { ?s ?p ?o }"), nil)
	require.NoError(t, err)
	req.Header.Set("Accept", sparqlResultsJSON)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestEndpointQueriesTheTableAtASnapshot(t *testing.T) {
	runner := &endpointRunner{
		snapshots: []int64{1234, 5678},
		result: salsparql.Result{
			Header: []string{"s"},
			Rows:   [][]string{{"https://example.org/alice"}},
		},
	}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	resp := snapshotGet(t, server.URL, "/v1234/sparql")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, sparqlResultsJSON, resp.Header.Get("Content-Type"))
	require.Equal(t, int64(1234), runner.snapshot)
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)

	var body sparqlJSONResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "https://example.org/alice", body.Results.Bindings[0]["s"].Value)
}

func TestEndpointAnswersNotFoundForAnUnknownSnapshot(t *testing.T) {
	runner := &endpointRunner{snapshots: []int64{1234}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	resp := snapshotGet(t, server.URL, "/v99/sparql")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "no snapshot 99")
	require.Empty(t, runner.query, "nothing must be run against a snapshot the table does not have")
}

func TestEndpointAnswersNotFoundForAPathThatIsNotASnapshotVersion(t *testing.T) {
	runner := &endpointRunner{snapshots: []int64{1234}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	for _, path := range []string{"/v12abc/sparql", "/v-1/sparql", "/v0/sparql", "/v/sparql"} {
		resp := snapshotGet(t, server.URL, path)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		require.Contains(t, string(body), "/v<snapshot id>/sparql", path)
	}
	require.Empty(t, runner.query)
}

// Only /v{snapshot}/sparql is the versioned route; anything else at the root
// keeps going where it went before, which for the UI is the app itself.
func TestEndpointWithUIStillServesTheAppBesideTheVersionedRoute(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	for _, path := range []string{"/v1234", "/v1234/sparql/extra", "/stats", "/latest/sparql"} {
		resp := browserGet(t, server.URL+path)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.Contains(t, resp.Header.Get("Content-Type"), "text/html", path)
	}
}

func TestEndpointReportsASnapshotLookupFailure(t *testing.T) {
	runner := &endpointRunner{snapshotErr: fmt.Errorf("duckdb query failed: no such table")}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	resp := snapshotGet(t, server.URL, "/v1234/sparql")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "no such table")
}

// A preflight is answered whatever the snapshot, so that a browser client gets
// the 404 for an unknown one rather than a CORS failure.
func TestEndpointAnswersPreflightForAnyVersionedSparqlPath(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", ""))
	defer server.Close()

	req, err := http.NewRequest(http.MethodOptions, server.URL+"/v99/sparql", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestEndpointWithUIQueriesTheTableAtASnapshotByPOST(t *testing.T) {
	runner := &endpointUIRunner{endpointRunner: endpointRunner{
		snapshots: []int64{7847372623449923428},
		result:    salsparql.Result{Header: []string{"s"}},
	}}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v7847372623449923428/sparql", "application/sparql-query", strings.NewReader("SELECT ?s WHERE { ?s ?p ?o }"))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, sparqlResultsJSON, resp.Header.Get("Content-Type"))
	require.Equal(t, int64(7847372623449923428), runner.snapshot)
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)
}

func TestEndpointWithUIAnswersNotFoundForAnUnknownSnapshot(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp := snapshotGet(t, server.URL, "/v1234/sparql")
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/plain")
}

// newStacDir writes the catalog and collection a build leaves under
// .sal/data/stac into a temporary directory.
func newStacDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(`{"type":"Catalog","id":"widgets"}`), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "triples"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "triples", "collection.json"), []byte(`{"type":"Collection","id":"triples"}`), 0644))
	return dir
}

func getStac(t *testing.T, server *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(server.URL + path)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func TestStacEndpointServesTheCatalogAndItsCollection(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", newStacDir(t)))
	defer server.Close()

	for _, path := range []string{"/stac", "/stac/", "/stac/catalog.json"} {
		resp, body := getStac(t, server, path)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.Equal(t, "application/json", resp.Header.Get("Content-Type"), path)
		require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"), path)
		require.JSONEq(t, `{"type":"Catalog","id":"widgets"}`, body, path)
	}

	resp, body := getStac(t, server, "/stac/triples/collection.json")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{"type":"Collection","id":"triples"}`, body)
}

func TestStacEndpointSaysToBuildWhenThereIsNoCatalog(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", filepath.Join(t.TempDir(), "missing")))
	defer server.Close()

	resp, body := getStac(t, server, "/stac/catalog.json")

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, body, "sal build")
}

func TestStacEndpointServesOnlyJSONInsideTheCatalog(t *testing.T) {
	dir := newStacDir(t)
	secret := filepath.Join(filepath.Dir(dir), "secret.json")
	require.NoError(t, os.WriteFile(secret, []byte(`{"secret":true}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a document"), 0644))
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", dir))
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, path := range []string{"/stac/../secret.json", "/stac/%2e%2e/secret.json", "/stac/notes.txt", "/stac/triples"} {
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		// ServeMux answers a path with ".." by redirecting to its cleaned form,
		// so the refusal may be a redirect rather than a 404; either way the
		// document outside the catalog is never served
		require.NotEqual(t, http.StatusOK, resp.StatusCode, path)
		require.NotContains(t, string(body), `"secret":true`, path)
	}
}

func TestStacEndpointRefusesOtherMethods(t *testing.T) {
	server := httptest.NewServer(NewEndpoint(&endpointRunner{}, "", newStacDir(t)))
	defer server.Close()

	resp, err := http.Post(server.URL+"/stac/catalog.json", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)

	req, err := http.NewRequest(http.MethodOptions, server.URL+"/stac/catalog.json", nil)
	require.NoError(t, err)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestEndpointWithUIServesTheStacCatalogRatherThanTheApp(t *testing.T) {
	handler, err := NewEndpointWithUI(&endpointUIRunner{}, "", newStacDir(t))
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/stac/catalog.json", nil)
	require.NoError(t, err)
	// a browser following the Open as STAC link asks for HTML, and still gets the catalog
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.JSONEq(t, `{"type":"Catalog","id":"widgets"}`, string(body))
}

func TestEndpointPassesReasoningFromAGETParameter(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/sparql?reasoning=true&query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, runner.reasoning)
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)

	// absent, it is off
	resp, err = http.Get(server.URL + "/sparql?query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.False(t, runner.reasoning)
}

func TestEndpointPassesReasoningFromAFormField(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	resp, err := http.Post(server.URL+"/sparql", "application/x-www-form-urlencoded",
		strings.NewReader("query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D&reasoning=true"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, runner.reasoning)
}

func TestEndpointPassesReasoningFromTheURLOfASparqlQueryPOST(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/sparql?reasoning=true", strings.NewReader("SELECT ?s WHERE { ?s ?p ?o }"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/sparql-query")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, runner.reasoning)
	require.Equal(t, "SELECT ?s WHERE { ?s ?p ?o }", runner.query)
}

func TestEndpointRejectsAReasoningValueThatIsNotABoolean(t *testing.T) {
	runner := &endpointRunner{result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/sparql?reasoning=rdfs&query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "the reasoning parameter must be true or false")
	require.Empty(t, runner.query, "nothing must run")
}

func TestEndpointQueriesASnapshotWithReasoning(t *testing.T) {
	runner := &endpointRunner{snapshots: []int64{1234}, result: salsparql.Result{Header: []string{"s"}}}
	server := httptest.NewServer(NewEndpoint(runner, "", ""))
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1234/sparql?reasoning=true&query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int64(1234), runner.snapshot)
	require.True(t, runner.reasoning)
}

func TestEndpointWithUITranslatesWithReasoning(t *testing.T) {
	runner := &endpointUIRunner{}
	server := newUIServer(t, runner)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/sparql/translate", "application/json",
		strings.NewReader(`{"query":"SELECT ?s WHERE { ?s <http://www.w3.org/2000/01/rdf-schema#subClassOf> ?o } LIMIT 5","reasoning":true}`))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		SQL string `json:"sql"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body.SQL, "FROM triples_all AS t0")
}
