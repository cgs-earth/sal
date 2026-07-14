package load

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
)

// stabilizeBlankNodes replaces parser-generated blank node IDs with deterministic
// IDs derived from the graph structure. This is not full RDF canonicalization; it
// only gives SAL stable row hashes without serializing through N-Quads, which
// rejects relative IRIs that SAL accepts.
func stabilizeBlankNodes(graph *rdflibgo.Graph) *rdflibgo.Graph {
	triples := graphTriples(graph)
	blankIDs := graphBlankNodeIDs(triples)
	if len(blankIDs) == 0 {
		return graph
	}

	replacements := stableBlankNodeIDs(triples, blankIDs)
	stable := rdflibgo.NewGraph(rdflibgo.WithBase(graph.Base()))
	for _, triple := range triples {
		stable.Add(
			replaceBlankSubject(triple.Subject, replacements),
			triple.Predicate,
			replaceBlankTerm(triple.Object, replacements),
		)
	}
	return stable
}

func graphTriples(graph *rdflibgo.Graph) []rdflibgo.Triple {
	var triples []rdflibgo.Triple
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		triples = append(triples, triple)
		return true
	})
	return triples
}

func graphBlankNodeIDs(triples []rdflibgo.Triple) []string {
	seen := map[string]struct{}{}
	for _, triple := range triples {
		if blank, ok := triple.Subject.(rdflibgo.BNode); ok {
			seen[blank.Value()] = struct{}{}
		}
		if blank, ok := triple.Object.(rdflibgo.BNode); ok {
			seen[blank.Value()] = struct{}{}
		}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// stableBlankNodeIDs computes a deterministic replacement ID for each blank node
// from the sorted signatures of triples touching that blank node.
func stableBlankNodeIDs(triples []rdflibgo.Triple, blankIDs []string) map[string]string {
	signatures := map[string]string{}
	for _, id := range blankIDs {
		signatures[id] = "blank"
	}

	for pass := 0; pass <= len(blankIDs); pass++ {
		next := map[string]string{}
		for _, id := range blankIDs {
			next[id] = blankNodeSignature(id, triples, signatures)
		}
		if sameStringMap(signatures, next) {
			signatures = next
			break
		}
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

func blankNodeSignature(id string, triples []rdflibgo.Triple, previous map[string]string) string {
	entries := []string{}
	for _, triple := range triples {
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

func sameStringMap(a map[string]string, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
