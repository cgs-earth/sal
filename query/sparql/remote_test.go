package sparql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocateRemoteTableAcceptsAnObjectStorageURL(t *testing.T) {
	table, err := LocateRemoteTable(" gs://sal-test-bucket/sal/triples/ ")

	require.NoError(t, err)
	require.Equal(t, "gs://sal-test-bucket/sal/triples", table.Path)
	require.True(t, table.Remote())
	require.Empty(t, table.Imports)
}

func TestLocateRemoteTableAcceptsEveryUploadScheme(t *testing.T) {
	for _, location := range []string{
		"s3://bucket/sal/triples",
		"r2://bucket/sal/triples",
		"az://container/sal/triples",
		"https://storage.googleapis.com/bucket/sal/triples",
	} {
		_, err := LocateRemoteTable(location)
		require.NoError(t, err, location)
	}
}

func TestLocateRemoteTableRefusesALocalPath(t *testing.T) {
	for _, location := range []string{
		"/app/demo/.sal/data/sal-demo/triples",
		".sal/data/sal-demo/triples",
		"file:///app/demo/.sal/data/sal-demo/triples",
		"C:\\data\\triples",
		"",
	} {
		_, err := LocateRemoteTable(location)
		require.Error(t, err, location)
		require.ErrorContains(t, err, "not a table on object storage")
	}
}

func TestLocateRemoteTableRefusesAnUnencryptedOrUnknownScheme(t *testing.T) {
	for _, location := range []string{"http://example.org/sal/triples", "ftp://example.org/sal/triples", "gs:///triples"} {
		_, err := LocateRemoteTable(location)
		require.Error(t, err, location)
	}
}

func TestLocalTriplesTableIsNotRemote(t *testing.T) {
	require.False(t, TriplesTable{Warehouse: "/tmp/warehouse", Namespace: "demo", Path: "/tmp/warehouse/demo/triples"}.Remote())
	require.False(t, isRemoteTablePath("/tmp/warehouse/demo/triples"))
	require.True(t, isRemoteTablePath("gs://bucket/sal/triples"))
}

func TestVerifyRemoteObjectColumnsReportsATableThatCannotBeRead(t *testing.T) {
	// nothing listens for the bucket, so DuckDB fails to read the table's
	// metadata and the failure is reported against the location rather than
	// swallowed
	err := VerifyRemoteObjectColumns(context.Background(), "gs://sal-no-such-bucket-for-tests/sal/triples")

	require.Error(t, err)
	require.ErrorContains(t, err, "gs://sal-no-such-bucket-for-tests/sal/triples")
}

func TestDescribeRunsThroughTheTextWrapper(t *testing.T) {
	// VerifyRemoteObjectColumns reads a DESCRIBE through queryRows, which wraps
	// every statement in a SELECT; DuckDB has to accept DESCRIBE as a subquery
	header, rows, err := queryRows(context.Background(), localDB(t), "DESCRIBE SELECT 1 AS object_language")

	require.NoError(t, err)
	require.Contains(t, header, "column_name")
	require.Len(t, rows, 1)
	require.Equal(t, "object_language", rows[0][0])
}

func TestLocateRemoteTableNamesTheBucketURLsForAConsoleLink(t *testing.T) {
	_, err := LocateRemoteTable("https://storage.cloud.google.com/sal-geo-repo-test-bucket/sal/triples/")

	require.Error(t, err)
	require.ErrorContains(t, err, "gs://sal-geo-repo-test-bucket/sal/triples")
	require.ErrorContains(t, err, "https://storage.googleapis.com/sal-geo-repo-test-bucket/sal/triples")
}
