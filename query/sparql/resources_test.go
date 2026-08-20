package sparql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// objectText is the expression every lookup reads an object through.
func objectText(alias string) string {
	return "COALESCE(" + alias + ".object_iri, CAST(" + alias + ".object_float AS VARCHAR), " + alias + ".object_string)"
}

func TestClassesSQLFiltersOnRDFType(t *testing.T) {
	sql := ClassesSQL()
	require.Contains(t, sql, "WHERE triples.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
	require.Contains(t, sql, objectText("triples")+" AS class")
	require.Contains(t, sql, "COUNT(DISTINCT triples.subject) AS instances")
}

func TestDatatypesSQLSelectsSubjectsTypedAsAnRDFSDatatype(t *testing.T) {
	sql := DatatypesSQL()
	require.Contains(t, sql, "datatypes.subject AS datatype")
	require.Contains(t, sql, "WHERE datatypes.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
	require.Contains(t, sql, "AND "+objectText("datatypes")+" = 'http://www.w3.org/2000/01/rdf-schema#Datatype'")
}

func TestDatatypesSQLLeftJoinsTheOptionalAnnotations(t *testing.T) {
	sql := DatatypesSQL()
	require.Contains(t, sql, "LEFT JOIN triples AS labels")
	require.Contains(t, sql, "AND labels.predicate = 'http://www.w3.org/2000/01/rdf-schema#label'")
	require.Contains(t, sql, "MIN("+objectText("labels")+") AS label")
	require.Contains(t, sql, "LEFT JOIN triples AS comments")
	require.Contains(t, sql, "AND comments.predicate = 'http://www.w3.org/2000/01/rdf-schema#comment'")
	require.Contains(t, sql, "MIN("+objectText("comments")+") AS comment")
}

func TestInstancesSQLPairsEachSubjectWithTheClassItIsTypedWith(t *testing.T) {
	sql := InstancesSQL()
	require.Contains(t, sql, "instances.subject AS instance")
	require.Contains(t, sql, objectText("instances")+" AS class")
	require.Contains(t, sql, "WHERE instances.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
}

func TestInstancesSQLExcludesSubjectsThatAreThemselvesVocabulary(t *testing.T) {
	sql := InstancesSQL()
	require.Contains(t, sql, "AND instances.subject NOT IN (")
	require.Contains(t, sql, "SELECT vocabulary.subject")
	require.Contains(t, sql, "AND "+objectText("vocabulary")+" IN ("+
		"'http://www.w3.org/2000/01/rdf-schema#Class', "+
		"'http://www.w3.org/2002/07/owl#Class', "+
		"'http://www.w3.org/2000/01/rdf-schema#Datatype', "+
		"'http://www.w3.org/1999/02/22-rdf-syntax-ns#Property', "+
		"'http://www.w3.org/2002/07/owl#ObjectProperty', "+
		"'http://www.w3.org/2002/07/owl#DatatypeProperty', "+
		"'http://www.w3.org/2002/07/owl#AnnotationProperty', "+
		"'http://www.w3.org/2002/07/owl#Ontology')")
}

func TestShapesSQLSelectsSubjectsTypedAsASHACLShape(t *testing.T) {
	sql := ShapesSQL()
	require.Contains(t, sql, "shapes.subject AS shape")
	require.Contains(t, sql, objectText("shapes")+` AS "rdf:type"`)
	require.Contains(t, sql, "WHERE shapes.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
	require.Contains(t, sql, "AND "+objectText("shapes")+" IN ("+
		"'http://www.w3.org/ns/shacl#NodeShape', "+
		"'http://www.w3.org/ns/shacl#PropertyShape')")
}

func TestShapesSQLLeftJoinsTheAnnotationsAndTheTargetClass(t *testing.T) {
	sql := ShapesSQL()
	require.Contains(t, sql, "LEFT JOIN triples AS labels")
	require.Contains(t, sql, "MIN("+objectText("labels")+`) AS "rdfs:label"`)
	require.Contains(t, sql, "LEFT JOIN triples AS comments")
	require.Contains(t, sql, "MIN("+objectText("comments")+`) AS "rdfs:comment"`)
	require.Contains(t, sql, "LEFT JOIN triples AS targets")
	require.Contains(t, sql, "AND targets.predicate = 'http://www.w3.org/ns/shacl#targetClass'")
	require.Contains(t, sql, objectText("targets")+` AS "sh:targetClass"`)
}

// Every column but the shape itself is named with the prefixed form of the
// predicate it reports, so the table says which term each value was read from.
func TestShapesSQLNamesEachPredicateColumnWithItsPrefixedTerm(t *testing.T) {
	sql := ShapesSQL()
	require.Contains(t, sql, `AS "rdfs:label"`)
	require.Contains(t, sql, `AS "rdfs:comment"`)
	require.Contains(t, sql, `AS "rdf:type"`)
	require.Contains(t, sql, `AS "sh:targetClass"`)
	require.NotContains(t, sql, "AS label,")
	require.NotContains(t, sql, "AS comment,")
	require.NotContains(t, sql, "AS type,")
	require.NotContains(t, sql, `AS "targetClass"`)
}

// A shape can state more than one sh:targetClass, so the target is grouped by
// rather than aggregated and the shape is listed once per class it targets.
func TestShapesSQLGroupsByTheTargetClassRatherThanAggregatingIt(t *testing.T) {
	sql := ShapesSQL()
	require.Contains(t, sql, `GROUP BY shape, "rdf:type", "sh:targetClass"`)
	require.NotContains(t, sql, "MIN("+objectText("targets")+")")
}

func TestDescribeSQLFiltersOnTheSubject(t *testing.T) {
	sql := DescribeSQL("https://geoconnex.us/ontologies/method/pastor")
	require.Contains(t, sql, "WHERE triples.subject = 'https://geoconnex.us/ontologies/method/pastor'")
	require.Contains(t, sql, "triples.predicate AS predicate")
	require.Contains(t, sql, objectText("triples")+" AS object")
}

func TestDescribeSQLEscapesQuotesInTheSubject(t *testing.T) {
	sql := DescribeSQL("https://example.org/o'brien")
	require.Contains(t, sql, "WHERE triples.subject = 'https://example.org/o''brien'")
}

func TestPropertiesSQLSelectsSubjectsTypedAsAProperty(t *testing.T) {
	sql := PropertiesSQL()
	require.Contains(t, sql, "properties.subject AS property")
	require.Contains(t, sql, objectText("properties")+" AS type")
	require.Contains(t, sql, "WHERE properties.predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'")
	require.Contains(t, sql, "AND "+objectText("properties")+" IN ("+
		"'http://www.w3.org/1999/02/22-rdf-syntax-ns#Property', "+
		"'http://www.w3.org/2002/07/owl#ObjectProperty', "+
		"'http://www.w3.org/2002/07/owl#DatatypeProperty', "+
		"'http://www.w3.org/2002/07/owl#AnnotationProperty')")
}
