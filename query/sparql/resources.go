package sparql

import "fmt"

// RDFTypeIRI is the predicate that states the class of a resource.
const RDFTypeIRI = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"

// The RDF Schema terms a datatype lookup reads: the class a resource is typed
// with to declare it a datatype, and the two annotations it optionally carries.
const (
	RDFSDatatypeIRI = "http://www.w3.org/2000/01/rdf-schema#Datatype"
	RDFSLabelIRI    = "http://www.w3.org/2000/01/rdf-schema#label"
	RDFSCommentIRI  = "http://www.w3.org/2000/01/rdf-schema#comment"
)

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
