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

func TestInstancesSQLPairsEachSubjectWithTheClassItIsTypedWith(t *testing.T) {
	sql := InstancesSQL(SimpleObjects)
	require.Contains(t, sql, "instances.subject AS instance")
	require.Contains(t, sql, "instances.object AS class")
	require.Contains(t, sql, "WHERE instances.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
}

func TestInstancesSQLExcludesSubjectsThatAreThemselvesVocabulary(t *testing.T) {
	sql := InstancesSQL(SimpleObjects)
	require.Contains(t, sql, "AND instances.subject NOT IN (")
	require.Contains(t, sql, "SELECT vocabulary.subject")
	require.Contains(t, sql, "AND vocabulary.object IN ("+
		"'http://www.w3.org/2000/01/rdf-schema#Class', "+
		"'http://www.w3.org/2002/07/owl#Class', "+
		"'http://www.w3.org/2000/01/rdf-schema#Datatype', "+
		"'http://www.w3.org/1999/02/22-rdf-syntax-ns#Property', "+
		"'http://www.w3.org/2002/07/owl#ObjectProperty', "+
		"'http://www.w3.org/2002/07/owl#DatatypeProperty', "+
		"'http://www.w3.org/2002/07/owl#AnnotationProperty', "+
		"'http://www.w3.org/2002/07/owl#Ontology')")
}

func TestInstancesSQLReadsTypedObjectColumns(t *testing.T) {
	sql := InstancesSQL(TypedObjects)
	require.Contains(t, sql, "COALESCE(instances.object_iri, CAST(instances.object_float AS VARCHAR), instances.object_string) AS class")
	require.Contains(t, sql, "AND COALESCE(vocabulary.object_iri, CAST(vocabulary.object_float AS VARCHAR), vocabulary.object_string) IN (")
	require.NotContains(t, sql, "instances.object AS class")
}

func TestDatatypesSQLReadsTypedObjectColumns(t *testing.T) {
	sql := DatatypesSQL(TypedObjects)
	require.Contains(t, sql, "MIN(COALESCE(labels.object_iri, CAST(labels.object_float AS VARCHAR), labels.object_string)) AS label")
	require.Contains(t, sql, "AND COALESCE(datatypes.object_iri, CAST(datatypes.object_float AS VARCHAR), datatypes.object_string) = 'http://www.w3.org/2000/01/rdf-schema#Datatype'")
	require.NotContains(t, sql, "datatypes.object =")
}

func TestDescribeSQLFiltersOnTheSubjectForSimpleObjects(t *testing.T) {
	sql := DescribeSQL("https://geoconnex.us/ontologies/method/pastor", SimpleObjects)
	require.Contains(t, sql, "WHERE triples.subject = 'https://geoconnex.us/ontologies/method/pastor'")
	require.Contains(t, sql, "triples.predicate AS predicate")
	require.Contains(t, sql, "triples.object AS object")
}

func TestDescribeSQLReadsTypedObjectColumns(t *testing.T) {
	sql := DescribeSQL("https://geoconnex.us/ontologies/method/pastor", TypedObjects)
	require.Contains(t, sql, "COALESCE(triples.object_iri, CAST(triples.object_float AS VARCHAR), triples.object_string) AS object")
	require.NotContains(t, sql, "triples.object AS object")
}

func TestDescribeSQLEscapesQuotesInTheSubject(t *testing.T) {
	sql := DescribeSQL("https://example.org/o'brien", SimpleObjects)
	require.Contains(t, sql, "WHERE triples.subject = 'https://example.org/o''brien'")
}
