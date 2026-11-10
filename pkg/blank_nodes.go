package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
)

// StableBlankNodeIDs computes a deterministic replacement label for each
// blank node in triples, every one of which touches a blank node, from the
// sorted signatures of the triples touching that blank node, refined pass by
// pass until the signatures of its neighbours stop splitting them. Each
// triple is indexed under every blank node in it, so a pass costs the size of
// that index rather than the number of blank nodes times the triples.
//
// The labels depend on triples alone, and since the number of passes does
// too, the label of a blank node depends on every blank node it is given
// with, not only the ones it is connected to. StableBlankNodeComponents is the
// form whose labels depend only on a node's own component.
func StableBlankNodeIDs(triples []rdflibgo.Triple) map[string]string {
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

	signatures := make(map[string]string, len(blankIDs))
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
		next := make(map[string]string, len(blankIDs))
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

	replacements := make(map[string]string, len(blankIDs))
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

// StableBlankNodeComponents relabels the blank nodes of triples, in place,
// one connected component at a time: the blank nodes a chain of triples
// joins, together with the IRIs and literals those triples name, are labelled
// by StableBlankNodeIDs over that component's triples alone. A component's
// labels therefore depend on nothing outside it, so the same node a SAL
// module writes gets the same labels whatever else was written around it, and
// a module's output can be stabilized as it is parsed, a batch at a time,
// rather than once over everything a build holds. Two components alike in
// every triple, IRIs included, get the same labels and so become one, which
// is what a set of rows keyed by their hash makes of them anyway.
func StableBlankNodeComponents(triples []rdflibgo.Triple) {
	parent := map[string]string{}
	var find func(string) string
	find = func(id string) string {
		root, ok := parent[id]
		if !ok {
			parent[id] = id
			return id
		}
		if root == id {
			return id
		}
		root = find(root)
		parent[id] = root
		return root
	}
	var blankTriples []int
	for i, triple := range triples {
		subject, subjectBlank := triple.Subject.(rdflibgo.BNode)
		object, objectBlank := triple.Object.(rdflibgo.BNode)
		switch {
		case subjectBlank && objectBlank:
			a, b := find(subject.Value()), find(object.Value())
			if a != b {
				parent[a] = b
			}
		case subjectBlank:
			find(subject.Value())
		case objectBlank:
			find(object.Value())
		default:
			continue
		}
		blankTriples = append(blankTriples, i)
	}
	if len(blankTriples) == 0 {
		return
	}

	components := map[string][]int{}
	var roots []string
	for _, i := range blankTriples {
		triple := triples[i]
		var root string
		if blank, ok := triple.Subject.(rdflibgo.BNode); ok {
			root = find(blank.Value())
		} else {
			root = find(triple.Object.(rdflibgo.BNode).Value())
		}
		if _, ok := components[root]; !ok {
			roots = append(roots, root)
		}
		components[root] = append(components[root], i)
	}

	component := []rdflibgo.Triple{}
	lines := []string{}
	for _, root := range roots {
		component = component[:0]
		for _, i := range components[root] {
			component = append(component, triples[i])
		}
		// the refinement only tells the nodes of the component apart from
		// each other: it stops once a pass splits nothing, so a lone blank
		// node keeps the signature every lone blank node starts with. The
		// label is therefore taken from a digest of the whole component as
		// those labels write it, every IRI and literal in it included, which
		// is what tells it apart from every other component
		replacements := StableBlankNodeIDs(component)
		lines = lines[:0]
		for _, triple := range component {
			lines = append(lines, termSignature(ReplaceBlankNode(triple.Subject, replacements), nil)+"|"+triple.Predicate.String()+"|"+termSignature(ReplaceBlankNode(triple.Object, replacements), nil))
		}
		sort.Strings(lines)
		digest := hashString(strings.Join(lines, "\n"))
		for id, label := range replacements {
			replacements[id] = "sal_" + hashString(digest + "|" + label)[:24]
		}
		for _, i := range components[root] {
			triples[i] = rdflibgo.Triple{
				Subject:   ReplaceBlankNode(triples[i].Subject, replacements).(rdflibgo.Subject),
				Predicate: triples[i].Predicate,
				Object:    ReplaceBlankNode(triples[i].Object, replacements),
			}
		}
	}
}

// ReplaceBlankNode returns the blank node replacements labels term with, or
// term itself when it is not a blank node.
func ReplaceBlankNode(term rdflibgo.Term, replacements map[string]string) rdflibgo.Term {
	if blank, ok := term.(rdflibgo.BNode); ok {
		return rdflibgo.NewBNode(replacements[blank.Value()])
	}
	return term
}

// blankNodeSignature hashes the triples at indexes touching, every one of
// which has the blank node id as its subject or object, with the signatures
// the previous pass gave the blank nodes they lead to.
func blankNodeSignature(id string, triples []rdflibgo.Triple, touching []int, previous map[string]string) string {
	entries := make([]string, 0, len(touching))
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
		if blankSignatures == nil {
			return "blank:" + value.Value()
		}
		return "blank:" + blankSignatures[value.Value()]
	case rdflibgo.Literal:
		return "literal:" + value.String() + "|datatype:" + value.Datatype().Value() + "|lang:" + value.Language()
	default:
		return "term:" + term.String()
	}
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
