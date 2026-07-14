package build

import (
	rdflibgo "github.com/tggo/goRDFlib"
)

// mergeGraph copies namespaces and triples from src into dst.
func mergeGraph(dst, src *rdflibgo.Graph) {
	src.Namespaces()(func(prefix string, ns rdflibgo.URIRef) bool {
		dst.Bind(prefix, ns)
		return true
	})
	src.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		dst.Add(triple.Subject, triple.Predicate, triple.Object)
		return true
	})
}
