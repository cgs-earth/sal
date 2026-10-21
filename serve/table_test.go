package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	salsparql "github.com/cgs-earth/sal/query/sparql"
	"github.com/stretchr/testify/require"
)

// fakeRemoteTables replaces openRemoteTable for a test with a function
// answering the runner for each location out of remotes, failing for any other.
func fakeRemoteTables(t *testing.T, remotes map[string]*endpointUIRunner) *[]string {
	t.Helper()
	original := openRemoteTable
	var opened []string
	openRemoteTable = func(_ context.Context, location string) (UIRunner, error) {
		opened = append(opened, location)
		runner, ok := remotes[location]
		if !ok {
			return nil, errors.New("cannot read the triples table at " + location)
		}
		return runner, nil
	}
	t.Cleanup(func() { openRemoteTable = original })
	return &opened
}

func putTable(t *testing.T, server *httptest.Server, location string) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]string{"remoteTablePath": location})
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPut, server.URL+"/api/table", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	return resp
}

func getStatsPath(t *testing.T, server *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(server.URL + "/api/stats")
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var stats salsparql.TableStats
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&stats))
	return stats.TablePath
}

func TestTableEndpointStartsOnTheLocalTable(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{stats: salsparql.TableStats{TablePath: "/tmp/warehouse/sal/triples"}})
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/table")
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body tableLocation
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.RemoteTablePath)
	require.Equal(t, "/tmp/warehouse/sal/triples", getStatsPath(t, server))
}

func TestTableEndpointSwitchesEveryQuerySurfaceToTheRemoteTable(t *testing.T) {
	remote := &endpointUIRunner{
		stats:     salsparql.TableStats{TablePath: "gs://bucket/sal/triples"},
		sqlResult: salsparql.Result{Header: []string{"n"}, Rows: [][]string{{"1"}}},
	}
	remote.result = salsparql.Result{Header: []string{"s"}, Rows: [][]string{{"https://example.org/remote"}}}
	local := &endpointUIRunner{stats: salsparql.TableStats{TablePath: "/tmp/warehouse/sal/triples"}}
	opened := fakeRemoteTables(t, map[string]*endpointUIRunner{"gs://bucket/sal/triples": remote})
	server := newUIServer(t, local)
	defer server.Close()

	resp := putTable(t, server, "gs://bucket/sal/triples")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body tableLocation
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "gs://bucket/sal/triples", body.RemoteTablePath)
	require.Equal(t, []string{"gs://bucket/sal/triples"}, *opened)

	require.Equal(t, "gs://bucket/sal/triples", getStatsPath(t, server))

	sqlResp, err := http.Post(server.URL+"/api/sql", "application/json", bytes.NewBufferString(`{"sql":"SELECT 1 AS n"}`))
	require.NoError(t, err)
	defer func() { require.NoError(t, sqlResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, sqlResp.StatusCode)
	require.Equal(t, "SELECT 1 AS n", remote.sql)
	require.Empty(t, local.sql)

	sparqlResp, err := http.Get(server.URL + "/sparql?query=" + "SELECT%20%3Fs%20WHERE%20%7B%20%3Fs%20%3Fp%20%3Fo%20%7D")
	require.NoError(t, err)
	defer func() { require.NoError(t, sparqlResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, sparqlResp.StatusCode)
	require.NotEmpty(t, remote.query)
	require.Empty(t, local.query)
}

func TestTableEndpointReturnsToTheLocalTableOnAnEmptyLocation(t *testing.T) {
	remote := &endpointUIRunner{stats: salsparql.TableStats{TablePath: "gs://bucket/sal/triples"}}
	fakeRemoteTables(t, map[string]*endpointUIRunner{"gs://bucket/sal/triples": remote})
	server := newUIServer(t, &endpointUIRunner{stats: salsparql.TableStats{TablePath: "/tmp/warehouse/sal/triples"}})
	defer server.Close()

	require.Equal(t, http.StatusOK, putTable(t, server, "gs://bucket/sal/triples").StatusCode)
	require.Equal(t, "gs://bucket/sal/triples", getStatsPath(t, server))

	resp := putTable(t, server, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body tableLocation
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Empty(t, body.RemoteTablePath)
	require.Equal(t, "/tmp/warehouse/sal/triples", getStatsPath(t, server))
}

func TestTableEndpointRefusesALocalPathWithoutOpeningIt(t *testing.T) {
	opened := fakeRemoteTables(t, nil)
	server := newUIServer(t, &endpointUIRunner{stats: salsparql.TableStats{TablePath: "/tmp/warehouse/sal/triples"}})
	defer server.Close()

	for _, location := range []string{"/etc/passwd", "file:///tmp/warehouse/sal/triples", "../other/triples"} {
		resp := putTable(t, server, location)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, location)
		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Contains(t, body["error"], "not a table on object storage")
	}
	// the location is refused by its shape alone, before the opener sees it
	require.Empty(t, *opened)
	require.Equal(t, "/tmp/warehouse/sal/triples", getStatsPath(t, server))
}

func TestTableEndpointKeepsTheCurrentTableWhenTheRemoteOneCannotBeRead(t *testing.T) {
	remote := &endpointUIRunner{stats: salsparql.TableStats{TablePath: "gs://bucket/sal/triples"}}
	fakeRemoteTables(t, map[string]*endpointUIRunner{"gs://bucket/sal/triples": remote})
	server := newUIServer(t, &endpointUIRunner{stats: salsparql.TableStats{TablePath: "/tmp/warehouse/sal/triples"}})
	defer server.Close()

	require.Equal(t, http.StatusOK, putTable(t, server, "gs://bucket/sal/triples").StatusCode)

	resp := putTable(t, server, "gs://bucket/missing/triples")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(t, body["error"], "cannot read the triples table at gs://bucket/missing/triples")
	require.Equal(t, "gs://bucket/sal/triples", getStatsPath(t, server))
}

func TestTableEndpointOnlyAnswersGETAndPUT(t *testing.T) {
	server := newUIServer(t, &endpointUIRunner{})
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/table", "application/json", bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	require.Equal(t, "GET, PUT", resp.Header.Get("Allow"))
}
