package sparql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// blobsTestTable is a triples table describing a pinned vocabulary, a file a
// module task handed over, a directory recorded from prov.jsonld, and an
// imported ontology whose own owl:versionIRI is an http IRI, which no blob
// answers to.
const blobsTestTable = `CREATE TABLE triples AS
	SELECT * FROM (VALUES
		('https://schema.org/', 'http://www.w3.org/2002/07/owl#versionIRI', 'urn:sha256:aaaa', NULL),
		('https://schema.org/', 'http://www.w3.org/2000/01/rdf-schema#label', NULL, 'https://schema.org/'),
		('https://schema.org/', 'http://www.w3.org/2002/07/owl#versionIRI', 'urn:sha256:bbbb', NULL),
		('salmodule://github.com/cgs-earth/demo', 'http://www.w3.org/2002/07/owl#versionIRI', 'urn:git-commit-hash:cccc', NULL),
		('urn:sha256:dddd', 'http://www.w3.org/2002/07/owl#versionIRI', 'urn:sha256:dddd', NULL),
		('urn:sha256:dddd', 'http://www.w3.org/2000/01/rdf-schema#label', NULL, 'file:///tmp/report.csv'),
		('urn:sha256:eeee', 'http://www.w3.org/2002/07/owl#versionIRI', 'urn:sha256:eeee', NULL),
		('urn:sha256:eeee', 'http://www.w3.org/2000/01/rdf-schema#label', NULL, 'file:///out/catalog/'),
		('https://ontology.test/', 'http://www.w3.org/2002/07/owl#versionIRI', 'https://ontology.test/1.0', NULL)
	) t(subject, predicate, object_iri, object_string),
	(SELECT NULL::DOUBLE AS object_float, NULL::BIGINT AS object_integer, NULL::INTEGER AS object_byte, NULL::TIMESTAMP AS object_time)`

func TestBlobsSQLListsEveryPinnedVersionAndCopiedFile(t *testing.T) {
	db := localDB(t)
	_, err := db.ExecContext(context.Background(), blobsTestTable)
	require.NoError(t, err)

	header, rows, err := queryRows(context.Background(), db, BlobsSQL(100))

	require.NoError(t, err)
	require.Equal(t, []string{"iri", "hash"}, header)
	require.Equal(t, [][]string{
		{"file:///out/catalog/", "urn:sha256:eeee"},
		{"file:///tmp/report.csv", "urn:sha256:dddd"},
		{"https://schema.org/", "urn:sha256:aaaa"},
		{"https://schema.org/", "urn:sha256:bbbb"},
		{"salmodule://github.com/cgs-earth/demo", "urn:git-commit-hash:cccc"},
	}, rows)
}

func TestBlobsSQLStopsAtTheLimit(t *testing.T) {
	db := localDB(t)
	_, err := db.ExecContext(context.Background(), blobsTestTable)
	require.NoError(t, err)

	_, rows, err := queryRows(context.Background(), db, BlobsSQL(2))

	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"file:///out/catalog/", "urn:sha256:eeee"},
		{"file:///tmp/report.csv", "urn:sha256:dddd"},
	}, rows)
}
