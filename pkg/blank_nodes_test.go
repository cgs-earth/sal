package pkg

import (
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

var (
	geo       = rdflibgo.NewURIRefUnsafe("https://schema.org/geo")
	latitude  = rdflibgo.NewURIRefUnsafe("https://schema.org/latitude")
	direction = rdflibgo.NewURIRefUnsafe("https://example.org/direction")
)

// feature is the triples one harvested node makes: a feature with a lone
// blank coordinate node, and a blank measure node leading to a blank node
// every feature shares the contents of.
func feature(iri string, lat string, blank string) []rdflibgo.Triple {
	subject := rdflibgo.NewURIRefUnsafe(iri)
	return []rdflibgo.Triple{
		{Subject: subject, Predicate: geo, Object: rdflibgo.NewBNode(blank + "geo")},
		{Subject: rdflibgo.NewBNode(blank + "geo"), Predicate: latitude, Object: rdflibgo.NewLiteral(lat)},
		{Subject: subject, Predicate: direction, Object: rdflibgo.NewBNode(blank + "measure")},
		{Subject: rdflibgo.NewBNode(blank + "measure"), Predicate: direction, Object: rdflibgo.NewBNode(blank + "shared")},
		{Subject: rdflibgo.NewBNode(blank + "shared"), Predicate: latitude, Object: rdflibgo.NewLiteral("upstream")},
	}
}

func distinctTriples(triples []rdflibgo.Triple) int {
	seen := map[string]bool{}
	for _, triple := range triples {
		seen[triple.Subject.String()+" "+triple.Predicate.String()+" "+triple.Object.String()] = true
	}
	return len(seen)
}

// Each feature's blank nodes are told apart from every other feature's by
// what the feature says, even a lone blank node, which the refinement alone
// leaves with the signature every lone blank node starts with, and a blank
// node whose own contents are the same in every feature.
func TestStableBlankNodeComponentsKeepsDistinctNodesApart(t *testing.T) {
	triples := append(feature("https://example.org/a", "41.0", "b0"), feature("https://example.org/b", "42.0", "b1")...)

	StableBlankNodeComponents(triples)

	require.Equal(t, 10, distinctTriples(triples))
}

// A node's labels depend on the node alone, not on its batch or on the
// labels the JSON-LD processor happened to give it, so the same node a
// module writes is the same rows in every build.
func TestStableBlankNodeComponentsLabelsANodeTheSameWhereverItIs(t *testing.T) {
	alone := feature("https://example.org/a", "41.0", "b0")
	StableBlankNodeComponents(alone)
	inABatch := append(feature("https://example.org/z", "1.0", "b0"), feature("https://example.org/a", "41.0", "b7")...)
	StableBlankNodeComponents(inABatch)

	require.Equal(t, alone, inABatch[5:])
}

// Two nodes alike in every triple, IRIs included, are one set of rows.
func TestStableBlankNodeComponentsMergesIdenticalNodes(t *testing.T) {
	triples := append(feature("https://example.org/a", "41.0", "b0"), feature("https://example.org/a", "41.0", "b1")...)

	StableBlankNodeComponents(triples)

	require.Equal(t, 5, distinctTriples(triples))
}

func TestStableBlankNodeComponentsLeavesTriplesWithoutBlankNodes(t *testing.T) {
	triples := []rdflibgo.Triple{{Subject: rdflibgo.NewURIRefUnsafe("https://example.org/a"), Predicate: latitude, Object: rdflibgo.NewLiteral("1")}}
	before := append([]rdflibgo.Triple{}, triples...)

	StableBlankNodeComponents(triples)

	require.Equal(t, before, triples)
}
