package load

import (
	"log/slog"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

// StabilizeBlankNodes replaces parser-generated blank node IDs with deterministic
// IDs derived from the graph structure (pkg.StableBlankNodeIDs). This is not
// full RDF canonicalization; it only gives SAL stable row hashes without
// serializing through N-Quads, which rejects relative IRIs that SAL accepts.
// The IDs depend on the graph alone, so stabilizing a graph that already is
// changes nothing.
//
// The graph is rewritten in place and returned: only the triples that touch a
// blank node are taken out and put back under the new labels, so a graph of
// millions of triples with a few blank nodes costs almost nothing, and no
// second copy of the graph is ever held. A SAL module's output never reaches
// this: it is stabilized a component at a time as it is parsed, see
// pkg.StableBlankNodeComponents.
func StabilizeBlankNodes(graph *rdflibgo.Graph) *rdflibgo.Graph {
	var triples []rdflibgo.Triple
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if isBlank(triple.Subject) || isBlank(triple.Object) {
			triples = append(triples, triple)
		}
		return true
	})
	if len(triples) == 0 {
		return graph
	}

	slog.Info("Deriving stable blank node labels from the graph structure", "blank_node_triples", len(triples))
	replacements := pkg.StableBlankNodeIDs(triples)
	// every old triple goes before any new one is added, since a renamed
	// triple may spell the same as one not yet removed
	for _, triple := range triples {
		pkg.RemoveTriples(graph, triple.Subject, &triple.Predicate, triple.Object)
	}
	for _, triple := range triples {
		pkg.AddTriple(graph,
			pkg.ReplaceBlankNode(triple.Subject, replacements).(rdflibgo.Subject),
			triple.Predicate,
			pkg.ReplaceBlankNode(triple.Object, replacements),
		)
	}
	return graph
}

func isBlank(term rdflibgo.Term) bool {
	_, ok := term.(rdflibgo.BNode)
	return ok
}
