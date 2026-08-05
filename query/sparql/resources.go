package sparql

import "fmt"

// RDFTypeIRI is the predicate that states the class of a resource.
const RDFTypeIRI = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"

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
