package sparql

import (
	"fmt"
	"strings"
)

// RDFTypeIRI is the predicate that states the class of a resource.
const RDFTypeIRI = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"

// The RDF Schema terms a datatype lookup reads: the class a resource is typed
// with to declare it a datatype, and the two annotations it optionally carries.
const (
	RDFSDatatypeIRI = "http://www.w3.org/2000/01/rdf-schema#Datatype"
	RDFSLabelIRI    = "http://www.w3.org/2000/01/rdf-schema#label"
	RDFSCommentIRI  = "http://www.w3.org/2000/01/rdf-schema#comment"
)

// The SHACL terms a shape lookup reads: the two classes a shape is typed with,
// and the predicate that states the class a shape is applied to.
const (
	SHNodeShapeIRI     = "http://www.w3.org/ns/shacl#NodeShape"
	SHPropertyShapeIRI = "http://www.w3.org/ns/shacl#PropertyShape"
	SHTargetClassIRI   = "http://www.w3.org/ns/shacl#targetClass"
)

// vocabularyClassIRIs are the classes a resource is typed with to declare that
// it is part of a vocabulary. A subject carrying one of them describes the
// schema rather than instantiating it, so an instance lookup leaves it out.
var vocabularyClassIRIs = []string{
	"http://www.w3.org/2000/01/rdf-schema#Class",
	"http://www.w3.org/2002/07/owl#Class",
	RDFSDatatypeIRI,
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#Property",
	"http://www.w3.org/2002/07/owl#ObjectProperty",
	"http://www.w3.org/2002/07/owl#DatatypeProperty",
	"http://www.w3.org/2002/07/owl#AnnotationProperty",
	"http://www.w3.org/2002/07/owl#Ontology",
}

// ClassesSQL lists every RDF class the data product types a resource with,
// most instantiated first. A class is the object of an rdf:type statement, so
// this is a filter on the predicate column rather than a schema lookup.
func ClassesSQL(layout ObjectLayout) string {
	return fmt.Sprintf(`
SELECT
	%s AS class,
	COUNT(DISTINCT triples.subject) AS instances
FROM triples
WHERE triples.predicate = '%s'
GROUP BY class
ORDER BY instances DESC, class`, bindingExpr("triples", "object", layout), RDFTypeIRI)
}

// DatatypesSQL lists every resource the data product declares to be an
// rdfs:Datatype, with the label and comment it is annotated with. Both
// annotations are optional, so they are left joined and come back empty for a
// datatype that does not state them.
func DatatypesSQL(layout ObjectLayout) string {
	return fmt.Sprintf(`
SELECT
	datatypes.subject AS datatype,
	MIN(%s) AS label,
	MIN(%s) AS comment
FROM triples AS datatypes
LEFT JOIN triples AS labels
	ON labels.subject = datatypes.subject
	AND labels.predicate = '%s'
LEFT JOIN triples AS comments
	ON comments.subject = datatypes.subject
	AND comments.predicate = '%s'
WHERE datatypes.predicate = '%s'
	AND %s = '%s'
GROUP BY datatype
ORDER BY datatype`,
		bindingExpr("labels", "object", layout),
		bindingExpr("comments", "object", layout),
		RDFSLabelIRI,
		RDFSCommentIRI,
		RDFTypeIRI,
		bindingExpr("datatypes", "object", layout),
		RDFSDatatypeIRI)
}

// DescribeSQL lists every statement the data product makes about one subject.
// It is the `<subject> ?p ?o` pattern, and since the subject is bound it stays a
// filter on the subject column rather than going through SPARQL translation.
func DescribeSQL(subject string, layout ObjectLayout) string {
	return fmt.Sprintf(`
SELECT
	triples.predicate AS predicate,
	%s AS object
FROM triples
WHERE triples.subject = %s
ORDER BY predicate, object`, bindingExpr("triples", "object", layout), sqlString(subject))
}

// InstancesSQL pairs every resource the data product instantiates with the
// class it is typed with. The class is not required to be declared an
// rdfs:Class or an owl:Class in the data product itself, since a data product
// commonly types its resources with a vocabulary it does not carry. What is
// filtered out instead is the other direction: a subject that is itself a
// class, property, datatype, or ontology describes the schema and is not an
// instance of it.
func InstancesSQL(layout ObjectLayout) string {
	quoted := make([]string, 0, len(vocabularyClassIRIs))
	for _, iri := range vocabularyClassIRIs {
		quoted = append(quoted, fmt.Sprintf("'%s'", iri))
	}
	return fmt.Sprintf(`
SELECT DISTINCT
	instances.subject AS instance,
	%s AS class
FROM triples AS instances
WHERE instances.predicate = '%s'
	AND instances.subject NOT IN (
		SELECT vocabulary.subject
		FROM triples AS vocabulary
		WHERE vocabulary.predicate = '%s'
			AND %s IN (%s)
	)
ORDER BY class, instance`,
		bindingExpr("instances", "object", layout),
		RDFTypeIRI,
		RDFTypeIRI,
		bindingExpr("vocabulary", "object", layout),
		strings.Join(quoted, ", "))
}

// ShapesSQL lists every SHACL shape the data product declares, with the
// annotations it carries and the class it targets. A shape is a subject typed
// sh:NodeShape or sh:PropertyShape, so the type it is reported with is the
// object of that statement rather than something derived; a shape declared to
// be both is listed once per type.
//
// The annotations and the target are all optional, so they are left joined and
// come back empty for a shape that does not state them. sh:targetClass is the
// one that can be stated more than once, and is grouped by rather than
// aggregated so that a shape targeting several classes is listed once per
// class, the way an instance typed with several classes is.
//
// Every column but the shape itself reports the object of a predicate, and is
// named with the prefixed form of that predicate, so the table says which term
// each value was read from rather than leaving `label` to stand for whichever
// of the several labelling predicates a vocabulary offers.
func ShapesSQL(layout ObjectLayout) string {
	return fmt.Sprintf(`
SELECT
	shapes.subject AS shape,
	MIN(%s) AS "rdfs:label",
	MIN(%s) AS "rdfs:comment",
	%s AS "rdf:type",
	%s AS "sh:targetClass"
FROM triples AS shapes
LEFT JOIN triples AS labels
	ON labels.subject = shapes.subject
	AND labels.predicate = '%s'
LEFT JOIN triples AS comments
	ON comments.subject = shapes.subject
	AND comments.predicate = '%s'
LEFT JOIN triples AS targets
	ON targets.subject = shapes.subject
	AND targets.predicate = '%s'
WHERE shapes.predicate = '%s'
	AND %s IN ('%s', '%s')
GROUP BY shape, "rdf:type", "sh:targetClass"
ORDER BY shape, "rdf:type", "sh:targetClass"`,
		bindingExpr("labels", "object", layout),
		bindingExpr("comments", "object", layout),
		bindingExpr("shapes", "object", layout),
		bindingExpr("targets", "object", layout),
		RDFSLabelIRI,
		RDFSCommentIRI,
		SHTargetClassIRI,
		RDFTypeIRI,
		bindingExpr("shapes", "object", layout),
		SHNodeShapeIRI,
		SHPropertyShapeIRI)
}
