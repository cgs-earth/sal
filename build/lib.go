package build

import (
	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

// mergeGraph copies namespaces and triples from src into dst.
func mergeGraph(dst, src *rdflibgo.Graph) {
	pkg.Namespaces(src)(func(prefix string, ns rdflibgo.URIRef) bool {
		pkg.Bind(dst, prefix, ns)
		return true
	})
	pkg.Triples(src, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		pkg.AddTriple(dst, triple.Subject, triple.Predicate, triple.Object)
		return true
	})
}
