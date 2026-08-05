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

func TestDatatypesSQLSelectsSubjectsTypedAsAnRDFSDatatype(t *testing.T) {
	sql := DatatypesSQL(SimpleObjects)
	require.Contains(t, sql, "datatypes.subject AS datatype")
	require.Contains(t, sql, "WHERE datatypes.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
	require.Contains(t, sql, "AND datatypes.object = 'http://www.w3.org/2000/01/rdf-schema#Datatype'")
}

func TestDatatypesSQLLeftJoinsTheOptionalAnnotations(t *testing.T) {
	sql := DatatypesSQL(SimpleObjects)
	require.Contains(t, sql, "LEFT JOIN triples AS labels")
	require.Contains(t, sql, "AND labels.predicate = 'http://www.w3.org/2000/01/rdf-schema#label'")
	require.Contains(t, sql, "MIN(labels.object) AS label")
	require.Contains(t, sql, "LEFT JOIN triples AS comments")
	require.Contains(t, sql, "AND comments.predicate = 'http://www.w3.org/2000/01/rdf-schema#comment'")
	require.Contains(t, sql, "MIN(comments.object) AS comment")
}

func TestDatatypesSQLReadsTypedObjectColumns(t *testing.T) {
	sql := DatatypesSQL(TypedObjects)
	require.Contains(t, sql, "MIN(COALESCE(labels.object_iri, CAST(labels.object_float AS VARCHAR), labels.object_string)) AS label")
	require.Contains(t, sql, "AND COALESCE(datatypes.object_iri, CAST(datatypes.object_float AS VARCHAR), datatypes.object_string) = 'http://www.w3.org/2000/01/rdf-schema#Datatype'")
	require.NotContains(t, sql, "datatypes.object =")
}
