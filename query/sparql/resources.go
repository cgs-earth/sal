package sparql

import (
	"fmt"
	"slices"
	"strings"
)

// RDFTypeIRI is the predicate that states the class of a resource.
const RDFTypeIRI = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"

// The RDF Schema and OWL terms the class and datatype lookups read: the classes
// a resource is typed with to declare it a class or a datatype, and the two
// annotations it optionally carries.
const (
	RDFSClassIRI    = "http://www.w3.org/2000/01/rdf-schema#Class"
	OWLClassIRI     = "http://www.w3.org/2002/07/owl#Class"
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

// propertyClassIRIs are the classes a resource is typed with to declare that it
// is a property. RDF states a plain property with rdf:Property, and OWL
// distinguishes the three kinds of property it defines, so a property lookup
// reports which of them a property was declared to be.
var propertyClassIRIs = []string{
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#Property",
	"http://www.w3.org/2002/07/owl#ObjectProperty",
	"http://www.w3.org/2002/07/owl#DatatypeProperty",
	"http://www.w3.org/2002/07/owl#AnnotationProperty",
}

// vocabularyClassIRIs are the classes a resource is typed with to declare that
// it is part of a vocabulary. A subject carrying one of them describes the
// schema rather than instantiating it, so an instance lookup leaves it out.
var vocabularyClassIRIs = slices.Concat(
	[]string{RDFSClassIRI, OWLClassIRI, RDFSDatatypeIRI},
	propertyClassIRIs,
	[]string{"http://www.w3.org/2002/07/owl#Ontology"},
)

// ClassesSQL lists every resource the data product declares to be a class,
// with the label and comment it is annotated with. A class is a subject typed
// rdfs:Class or owl:Class, so a class the data product only types resources
// with, without carrying its definition, is not listed; `sal get instances`
// is the lookup that reports those. Both annotations are optional, so they are
// left joined and come back empty for a class that does not state them, and a
// class declared to be both rdfs:Class and owl:Class is listed once.
//
// The annotation columns are named with the prefixed form of the predicate
// each one reports, the way `sal get shapes` names its columns.
func ClassesSQL() string {
	return fmt.Sprintf(`
SELECT
	classes.subject AS class,
	MIN(%s) AS "rdfs:label",
	MIN(%s) AS "rdfs:comment"
FROM triples AS classes
LEFT JOIN triples AS labels
	ON labels.subject = classes.subject
	AND labels.predicate = '%s'
LEFT JOIN triples AS comments
	ON comments.subject = classes.subject
	AND comments.predicate = '%s'
WHERE classes.predicate = '%s'
	AND %s IN ('%s', '%s')
GROUP BY class
ORDER BY class`,
		bindingExpr("labels", "object"),
		bindingExpr("comments", "object"),
		RDFSLabelIRI,
		RDFSCommentIRI,
		RDFTypeIRI,
		bindingExpr("classes", "object"),
		RDFSClassIRI,
		OWLClassIRI)
}

// DatatypesSQL lists every resource the data product declares to be an
// rdfs:Datatype, with the label and comment it is annotated with. Both
// annotations are optional, so they are left joined and come back empty for a
// datatype that does not state them. The annotation columns are named with the
// prefixed form of the predicate each one reports, the way `sal get classes`
// and `sal get shapes` name theirs.
func DatatypesSQL() string {
	return fmt.Sprintf(`
SELECT
	datatypes.subject AS datatype,
	MIN(%s) AS "rdfs:label",
	MIN(%s) AS "rdfs:comment"
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
		bindingExpr("labels", "object"),
		bindingExpr("comments", "object"),
		RDFSLabelIRI,
		RDFSCommentIRI,
		RDFTypeIRI,
		bindingExpr("datatypes", "object"),
		RDFSDatatypeIRI)
}

// DescribeSQL lists every statement the data product makes about one subject.
// It is the `<subject> ?p ?o` pattern, and since the subject is bound it stays a
// filter on the subject column rather than going through SPARQL translation.
func DescribeSQL(subject string) string {
	return fmt.Sprintf(`
SELECT
	triples.predicate AS predicate,
	%s AS object
FROM triples
WHERE triples.subject = %s
ORDER BY predicate, object`, bindingExpr("triples", "object"), sqlString(subject))
}

// InstancesSQL pairs every resource the data product instantiates with the
// class it is typed with. The class is not required to be declared an
// rdfs:Class or an owl:Class in the data product itself, since a data product
// commonly types its resources with a vocabulary it does not carry. What is
// filtered out instead is the other direction: a subject that is itself a
// class, property, datatype, or ontology describes the schema and is not an
// instance of it.
func InstancesSQL() string {
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
		bindingExpr("instances", "object"),
		RDFTypeIRI,
		RDFTypeIRI,
		bindingExpr("vocabulary", "object"),
		quotedIRIList(vocabularyClassIRIs))
}

// PropertiesSQL lists every resource the data product declares to be a
// property, with the class it was declared with. A property is a subject typed
// with one of rdf:Property, owl:ObjectProperty, owl:DatatypeProperty, or
// owl:AnnotationProperty, so the type it is reported with is the object of that
// statement; a property declared to be more than one of them is listed once per
// type, the way `sal get instances` lists a resource once per class.
func PropertiesSQL() string {
	return fmt.Sprintf(`
SELECT DISTINCT
	properties.subject AS property,
	%s AS type
FROM triples AS properties
WHERE properties.predicate = '%s'
	AND %s IN (%s)
ORDER BY property, type`,
		bindingExpr("properties", "object"),
		RDFTypeIRI,
		bindingExpr("properties", "object"),
		quotedIRIList(propertyClassIRIs))
}

// quotedIRIList renders IRIs as the comma separated SQL string literals a
// lookup's IN clause is built from.
func quotedIRIList(iris []string) string {
	quoted := make([]string, 0, len(iris))
	for _, iri := range iris {
		quoted = append(quoted, sqlString(iri))
	}
	return strings.Join(quoted, ", ")
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
func ShapesSQL() string {
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
		bindingExpr("labels", "object"),
		bindingExpr("comments", "object"),
		bindingExpr("shapes", "object"),
		bindingExpr("targets", "object"),
		RDFSLabelIRI,
		RDFSCommentIRI,
		SHTargetClassIRI,
		RDFTypeIRI,
		bindingExpr("shapes", "object"),
		SHNodeShapeIRI,
		SHPropertyShapeIRI)
}
