package load

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

// StabilizeBlankNodes replaces parser-generated blank node IDs with deterministic
// IDs derived from the graph structure. This is not full RDF canonicalization; it
// only gives SAL stable row hashes without serializing through N-Quads, which
// rejects relative IRIs that SAL accepts. The IDs depend on the graph alone,
// so stabilizing a graph that already is changes nothing.
//
// The graph is rewritten in place and returned: only the triples that touch a
// blank node are taken out and put back under the new labels, so a graph of
// millions of triples with a few blank nodes costs almost nothing, and no
// second copy of the graph is ever held.
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
	replacements := stableBlankNodeIDs(triples)
	slog.Info("Rewriting the triples that touch a blank node under their stable labels", "blank_nodes", len(replacements), "blank_node_triples", len(triples))
	// every old triple goes before any new one is added, since a renamed
	// triple may spell the same as one not yet removed
	for _, triple := range triples {
		pkg.RemoveTriples(graph, triple.Subject, &triple.Predicate, triple.Object)
	}
	for _, triple := range triples {
		pkg.AddTriple(graph,
			replaceBlankSubject(triple.Subject, replacements),
			triple.Predicate,
			replaceBlankTerm(triple.Object, replacements),
		)
	}
	return graph
}

func isBlank(term rdflibgo.Term) bool {
	_, ok := term.(rdflibgo.BNode)
	return ok
}

// stableBlankNodeIDs computes a deterministic replacement ID for each blank node
// from the sorted signatures of the triples touching that blank node, refined
// pass by pass until the signatures of its neighbours stop changing them.
// triples are the triples touching a blank node; each is indexed under every
// blank node in it, so a pass costs the size of that index rather than the
// number of blank nodes times the size of the graph.
func stableBlankNodeIDs(triples []rdflibgo.Triple) map[string]string {
	touching := map[string][]int{}
	for i, triple := range triples {
		if blank, ok := triple.Subject.(rdflibgo.BNode); ok {
			touching[blank.Value()] = append(touching[blank.Value()], i)
		}
		if blank, ok := triple.Object.(rdflibgo.BNode); ok {
			if subject, isSubject := triple.Subject.(rdflibgo.BNode); !isSubject || subject.Value() != blank.Value() {
				touching[blank.Value()] = append(touching[blank.Value()], i)
			}
		}
	}
	blankIDs := make([]string, 0, len(touching))
	for id := range touching {
		blankIDs = append(blankIDs, id)
	}
	sort.Strings(blankIDs)

	signatures := map[string]string{}
	for _, id := range blankIDs {
		signatures[id] = "blank"
	}

	// A pass hashes each blank node's triples with the signatures its
	// neighbours had after the previous pass, so it can only split the
	// classes of equally signed blank nodes, never merge them, and the
	// partition is final once a pass no longer splits one. The signatures
	// themselves never settle for a blank node that leads to another: each
	// pass hashes a neighbour's signature that the pass before changed, so
	// waiting for them to repeat would take a pass per blank node in a cycle,
	// each over every blank node triple.
	classes := 1
	for pass := 0; pass <= len(blankIDs); pass++ {
		next := map[string]string{}
		for _, id := range blankIDs {
			next[id] = blankNodeSignature(id, triples, touching[id], signatures)
		}
		nextClasses := distinctValues(next)
		if nextClasses == classes {
			break
		}
		classes = nextClasses
		signatures = next
	}

	groups := map[string][]string{}
	for id, signature := range signatures {
		groups[signature] = append(groups[signature], id)
	}
	signatureKeys := make([]string, 0, len(groups))
	for signature := range groups {
		signatureKeys = append(signatureKeys, signature)
	}
	sort.Strings(signatureKeys)

	replacements := map[string]string{}
	for _, signature := range signatureKeys {
		ids := groups[signature]
		sort.Strings(ids)
		signatureID := "sal_" + hashString(signature)[:24]
		for i, id := range ids {
			if len(ids) == 1 {
				replacements[id] = signatureID
				continue
			}
			replacements[id] = fmt.Sprintf("%s_%04d", signatureID, i)
		}
	}
	return replacements
}

// blankNodeSignature hashes the triples at indexes touching, every one of
// which has the blank node id as its subject or object, with the signatures
// the previous pass gave the blank nodes they lead to.
func blankNodeSignature(id string, triples []rdflibgo.Triple, touching []int, previous map[string]string) string {
	entries := []string{}
	for _, i := range touching {
		triple := triples[i]
		if blank, ok := triple.Subject.(rdflibgo.BNode); ok && blank.Value() == id {
			entries = append(entries, "subject|"+triple.Predicate.String()+"|"+termSignature(triple.Object, previous))
		}
		if blank, ok := triple.Object.(rdflibgo.BNode); ok && blank.Value() == id {
			entries = append(entries, "object|"+termSignature(triple.Subject, previous)+"|"+triple.Predicate.String())
		}
	}
	sort.Strings(entries)
	return hashString(strings.Join(entries, "\n"))
}

func termSignature(term rdflibgo.Term, blankSignatures map[string]string) string {
	switch value := term.(type) {
	case rdflibgo.URIRef:
		return "iri:" + value.Value()
	case rdflibgo.BNode:
		return "blank:" + blankSignatures[value.Value()]
	case rdflibgo.Literal:
		return "literal:" + value.String() + "|datatype:" + value.Datatype().Value() + "|lang:" + value.Language()
	default:
		return "term:" + term.String()
	}
}

func replaceBlankSubject(subject rdflibgo.Subject, replacements map[string]string) rdflibgo.Subject {
	if blank, ok := subject.(rdflibgo.BNode); ok {
		return rdflibgo.NewBNode(replacements[blank.Value()])
	}
	return subject
}

func replaceBlankTerm(term rdflibgo.Term, replacements map[string]string) rdflibgo.Term {
	if blank, ok := term.(rdflibgo.BNode); ok {
		return rdflibgo.NewBNode(replacements[blank.Value()])
	}
	return term
}

func distinctValues(m map[string]string) int {
	seen := make(map[string]struct{}, len(m))
	for _, value := range m {
		seen[value] = struct{}{}
	}
	return len(seen)
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
