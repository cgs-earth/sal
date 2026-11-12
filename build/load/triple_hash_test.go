package load

import rdflibgo "github.com/tggo/goRDFlib"

// tripleHash is digestTriple as a string of the 32 bytes the triple_hash
// column holds, which is what the tests compare a column's values against.
func tripleHash(subject string, predicate string, object string, datatype string, language string) string {
	digest := digestTriple(subject, predicate, object, datatype, language)
	return string(digest[:])
}

// tripleHashForTriple is tripleHash for a triple of a graph.
func tripleHashForTriple(triple rdflibgo.Triple) string {
	object := graphTripleObject(triple.Object)
	return tripleHash(storedSubject(triple.Subject), triple.Predicate.String(), object.o, object.oDatatype, object.oLanguage)
}
