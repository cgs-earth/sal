package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassesSQLFiltersOnRDFTypeForSimpleObjects(t *testing.T) {
	sql := ClassesSQL(SimpleObjects)
	require.Contains(t, sql, "WHERE triples.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
	require.Contains(t, sql, "triples.object AS class")
	require.Contains(t, sql, "COUNT(DISTINCT triples.subject) AS instances")
}

func TestClassesSQLReadsTypedObjectColumns(t *testing.T) {
	sql := ClassesSQL(TypedObjects)
	require.Contains(t, sql, "COALESCE(triples.object_iri, CAST(triples.object_float AS VARCHAR), triples.object_string) AS class")
	require.NotContains(t, sql, "triples.object AS class")
}
