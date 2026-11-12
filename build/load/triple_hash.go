package load

import (
	"crypto/sha256"

	rdflibgo "github.com/tggo/goRDFlib"
)

// tripleDigest is the SHA-256 that identifies a row, the 32 bytes the
// triple_hash column holds as a fixed[32].
type tripleDigest [sha256.Size]byte

// digestTriple returns a stable SHA-256 row identifier from the RDF triple
// terms. The object's datatype and language tag are part of the identity
// (both empty for an IRI or a blank node object, and the language empty for
// any literal but an rdf:langString), since the table stores them in
// object_type and object_language and a literal with the same lexical form
// but a different datatype or language is a different triple.
func digestTriple(subject string, predicate string, object string, datatype string, language string) tripleDigest {
	hash := sha256.New()
	_, _ = hash.Write([]byte("subject="))
	_, _ = hash.Write([]byte(subject))
	_, _ = hash.Write([]byte("\npredicate="))
	_, _ = hash.Write([]byte(predicate))
	_, _ = hash.Write([]byte("\nobject="))
	_, _ = hash.Write([]byte(object))
	_, _ = hash.Write([]byte("\ndatatype="))
	_, _ = hash.Write([]byte(datatype))
	_, _ = hash.Write([]byte("\nlanguage="))
	_, _ = hash.Write([]byte(language))
	var digest tripleDigest
	hash.Sum(digest[:0])
	return digest
}

// storedSubject renders a subject the way the triples table stores it: a blank
// node keeps N-Triples "_:" syntax so readers can tell it apart from an IRI,
// which SAL may store schemeless (relative) and therefore cannot distinguish
// by shape alone. The in-memory BNode keeps its bare label, since RDF
// serializers add the "_:" themselves.
func storedSubject(subject rdflibgo.Subject) string {
	if blank, ok := subject.(rdflibgo.BNode); ok {
		return "_:" + blank.Value()
	}
	return subject.String()
}

func graphTripleObject(object rdflibgo.Term) rdfObject {
	switch o := object.(type) {
	case rdflibgo.URIRef:
		return rdfObject{o: o.Value(), oKind: objectKindIRI}
	case rdflibgo.BNode:
		return rdfObject{o: "_:" + o.Value(), oKind: objectKindBNode}
	case rdflibgo.Literal:
		return rdfObject{o: o.String(), oKind: objectKindLiteral, oDatatype: o.Datatype().Value(), oLanguage: o.Language()}
	default:
		return rdfObject{o: object.String(), oKind: objectKindLiteral}
	}
}
