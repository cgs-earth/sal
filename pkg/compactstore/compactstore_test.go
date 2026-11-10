package compactstore

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/store"
	"github.com/tggo/goRDFlib/store/storetest"
	"github.com/tggo/goRDFlib/term"
)

var (
	name  = rdflibgo.NewURIRefUnsafe("https://schema.org/name")
	knows = rdflibgo.NewURIRefUnsafe("https://schema.org/knows")
	alice = rdflibgo.NewURIRefUnsafe("https://example.test/alice")
	bob   = rdflibgo.NewURIRefUnsafe("https://example.test/bob")
)

var ctx = context.Background()

// triples drains an iterator into sorted strings, failing on a store error.
func triples(t *testing.T, iterator store.TripleIterator) []string {
	t.Helper()
	var out []string
	for triple, err := range iterator {
		require.NoError(t, err)
		out = append(out, triple.String())
	}
	sort.Strings(out)
	return out
}

func count(t *testing.T, s store.Store) int {
	t.Helper()
	n, err := s.Len(ctx, nil)
	require.NoError(t, err)
	return n
}

func peopleStore(t *testing.T) *Store {
	t.Helper()
	s := New()
	require.NoError(t, s.Add(ctx, term.Triple{Subject: alice, Predicate: name, Object: rdflibgo.NewLiteral("Alice")}, nil))
	require.NoError(t, s.Add(ctx, term.Triple{Subject: alice, Predicate: knows, Object: bob}, nil))
	require.NoError(t, s.Add(ctx, term.Triple{Subject: bob, Predicate: name, Object: rdflibgo.NewLiteral("Bob")}, nil))
	require.NoError(t, s.Add(ctx, term.Triple{Subject: bob, Predicate: name, Object: rdflibgo.NewLiteral("Bob", rdflibgo.WithLang("en"))}, nil))
	return s
}

func TestAddHoldsATripleOnce(t *testing.T) {
	s := New()
	triple := term.Triple{Subject: alice, Predicate: name, Object: rdflibgo.NewLiteral("Alice")}

	require.NoError(t, s.Add(ctx, triple, nil))
	require.NoError(t, s.Add(ctx, triple, nil))

	require.Equal(t, 1, count(t, s))
	require.Len(t, s.terms, 3)
}

func TestTriplesMatchesEveryKindOfPattern(t *testing.T) {
	s := peopleStore(t)

	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{}, nil)), 4)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Subject: alice}, nil)), 2)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Predicate: &name}, nil)), 3)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Object: bob}, nil)), 1)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Subject: bob, Predicate: &name}, nil)), 2)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Predicate: &name, Object: rdflibgo.NewLiteral("Bob")}, nil)), 1)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Subject: alice, Object: bob}, nil)), 1)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Subject: alice, Predicate: &knows, Object: bob}, nil)), 1)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Subject: alice, Predicate: &knows, Object: alice}, nil)), 0)
}

// A language tagged literal and the plain literal with the same lexical form
// are different terms, as they are in MemoryStore.
func TestLiteralsDifferingOnlyInLanguageAreDistinct(t *testing.T) {
	s := peopleStore(t)

	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Object: rdflibgo.NewLiteral("Bob")}, nil)), 1)
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Object: rdflibgo.NewLiteral("Bob", rdflibgo.WithLang("en"))}, nil)), 1)
}

func TestPatternNamingAnUnknownTermMatchesNothing(t *testing.T) {
	s := peopleStore(t)

	require.Empty(t, triples(t, s.Triples(ctx, term.TriplePattern{Subject: rdflibgo.NewURIRefUnsafe("https://example.test/carol")}, nil)))
}

func TestRemoveDropsEveryMatchAndKeepsTheRest(t *testing.T) {
	s := peopleStore(t)

	require.NoError(t, s.Remove(ctx, term.TriplePattern{Subject: bob, Predicate: &name}, nil))

	require.Equal(t, 2, count(t, s))
	require.Equal(t, []string{
		"(<https://example.test/alice>, <https://schema.org/knows>, <https://example.test/bob>)",
		`(<https://example.test/alice>, <https://schema.org/name>, "Alice")`,
	}, triples(t, s.Triples(ctx, term.TriplePattern{}, nil)))
	require.Empty(t, triples(t, s.Triples(ctx, term.TriplePattern{Subject: bob}, nil)))
	exists, err := s.Exists(ctx, term.TriplePattern{Subject: bob, Predicate: &name, Object: rdflibgo.NewLiteral("Bob")}, nil)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestARemovedTripleCanBeAddedAgain(t *testing.T) {
	s := peopleStore(t)
	triple := term.Triple{Subject: alice, Predicate: knows, Object: bob}

	require.NoError(t, s.Remove(ctx, term.TriplePattern{Subject: alice, Predicate: &knows, Object: bob}, nil))
	require.NoError(t, s.Add(ctx, triple, nil))

	require.Equal(t, 4, count(t, s))
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Object: bob}, nil)), 1)
}

func TestSetReplacesTheValuesOfASubjectAndPredicate(t *testing.T) {
	s := peopleStore(t)

	require.NoError(t, s.Set(ctx, term.Triple{Subject: bob, Predicate: name, Object: rdflibgo.NewLiteral("Robert")}, nil))

	require.Equal(t, []string{`(<https://example.test/bob>, <https://schema.org/name>, "Robert")`}, triples(t, s.Triples(ctx, term.TriplePattern{Subject: bob}, nil)))
	require.Equal(t, 3, count(t, s))
}

func TestTriplesAreYieldedInInsertionOrder(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		require.NoError(t, s.Add(ctx, term.Triple{Subject: alice, Predicate: name, Object: rdflibgo.NewLiteral(fmt.Sprint(i))}, nil))
	}

	var seen []string
	for triple, err := range s.Triples(ctx, term.TriplePattern{}, nil) {
		require.NoError(t, err)
		seen = append(seen, triple.Object.(term.Literal).Lexical())
	}

	require.Equal(t, []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}, seen)
}

func TestIterationStopsWhenTheCallbackReturnsFalse(t *testing.T) {
	s := peopleStore(t)

	var seen int
	for _, err := range s.Triples(ctx, term.TriplePattern{}, nil) {
		require.NoError(t, err)
		seen++
		break
	}

	require.Equal(t, 1, seen)
	n, err := s.Count(ctx, term.TriplePattern{}, nil)
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Len(t, triples(t, s.TriplesWithLimit(ctx, term.TriplePattern{Predicate: &name}, nil, 2, 1)), 2)
}

// Removing more than half the store, the way blank node stabilization
// rewrites a graph, compacts the tombstones away without changing what the
// store holds or what its indexes find.
func TestCompactionKeepsEveryLiveTriple(t *testing.T) {
	s := New()
	var expected []string
	for i := 0; i < 3*compactionMinimum; i++ {
		predicate := name
		if i%3 == 0 {
			predicate = knows
		}
		triple := term.Triple{Subject: rdflibgo.NewBNode(fmt.Sprint(i)), Predicate: predicate, Object: rdflibgo.NewLiteral(fmt.Sprint(i))}
		require.NoError(t, s.Add(ctx, triple, nil))
		if i%3 == 0 {
			expected = append(expected, triple.String())
		}
	}
	sort.Strings(expected)

	require.NoError(t, s.Remove(ctx, term.TriplePattern{Predicate: &name}, nil))

	require.Equal(t, 0, s.removed)
	require.Len(t, s.triples, len(expected))
	require.Equal(t, expected, triples(t, s.Triples(ctx, term.TriplePattern{}, nil)))
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Object: rdflibgo.NewLiteral("3")}, nil)), 1)
	require.Empty(t, triples(t, s.Triples(ctx, term.TriplePattern{Object: rdflibgo.NewLiteral("4")}, nil)))
	require.Len(t, triples(t, s.Triples(ctx, term.TriplePattern{Predicate: &knows}, nil)), len(expected))
}

func TestNamespacesAreBoundBothWays(t *testing.T) {
	s := New()
	schema := rdflibgo.NewURIRefUnsafe("https://schema.org/")

	require.NoError(t, s.Bind(ctx, "schema", schema))

	namespace, ok, err := s.Namespace(ctx, "schema")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, schema, namespace)
	prefix, ok, err := s.Prefix(ctx, schema)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "schema", prefix)
	var bound []string
	for binding, err := range s.Namespaces(ctx) {
		require.NoError(t, err)
		bound = append(bound, binding.Prefix+"="+binding.Namespace.Value())
	}
	require.Equal(t, []string{"schema=https://schema.org/"}, bound)
	require.False(t, s.ContextAware())
	require.False(t, s.TransactionAware())
	for range s.Contexts(ctx, nil) {
		require.Fail(t, "a store with one unnamed graph has no contexts")
	}
}

// A done context is refused by every call, which is the one error the store
// can return.
func TestADoneContextIsRefused(t *testing.T) {
	s := peopleStore(t)
	done, cancel := context.WithCancel(ctx)
	cancel()

	require.ErrorIs(t, s.Add(done, term.Triple{Subject: alice, Predicate: name, Object: bob}, nil), context.Canceled)
	_, err := s.Len(done, nil)
	require.ErrorIs(t, err, context.Canceled)
	for _, err := range s.Triples(done, term.TriplePattern{}, nil) {
		require.ErrorIs(t, err, context.Canceled)
	}
	require.Equal(t, 4, count(t, s))
}

// TestStoreConformance runs goRDFlib's own store suite against the store. The
// suite's done-context check seeds one triple in the default graph and one in
// a named graph and counts each apart, which a store of one unnamed graph
// cannot do; every other check guards on ContextAware itself.
func TestStoreConformance(t *testing.T) {
	storetest.Run(t, storetest.Config{
		New: func(*testing.T) store.Store { return New() },
		Known: map[string]string{
			"DoneContext/writes are refused and change nothing": "the store holds one unnamed graph, so triples seeded in two graphs are counted together",
		},
	})
}

// The store answers every pattern the same way MemoryStore does over a random
// sequence of additions and removals, which is what lets a graph be built on
// either.
func TestStoreAgreesWithMemoryStore(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	subjects := []term.Subject{alice, bob, rdflibgo.NewBNode("b1"), rdflibgo.NewBNode("b2")}
	predicates := []term.URIRef{name, knows, rdflibgo.RDF.Type}
	objects := []term.Term{alice, bob, rdflibgo.NewBNode("b1"), rdflibgo.NewLiteral("x"), rdflibgo.NewLiteral("x", rdflibgo.WithLang("en")), rdflibgo.NewLiteral(1)}
	compact, memory := New(), store.NewMemoryStore()
	randomTriple := func() term.Triple {
		return term.Triple{Subject: subjects[random.Intn(len(subjects))], Predicate: predicates[random.Intn(len(predicates))], Object: objects[random.Intn(len(objects))]}
	}
	patterns := func(triple term.Triple) []term.TriplePattern {
		return []term.TriplePattern{
			{},
			{Subject: triple.Subject},
			{Predicate: &triple.Predicate},
			{Object: triple.Object},
			{Subject: triple.Subject, Predicate: &triple.Predicate},
			{Subject: triple.Subject, Object: triple.Object},
			{Predicate: &triple.Predicate, Object: triple.Object},
			{Subject: triple.Subject, Predicate: &triple.Predicate, Object: triple.Object},
		}
	}

	for step := 0; step < 2000; step++ {
		triple := randomTriple()
		switch random.Intn(4) {
		case 0:
			pattern := patterns(triple)[random.Intn(8)]
			require.NoError(t, compact.Remove(ctx, pattern, nil))
			require.NoError(t, memory.Remove(ctx, pattern, nil))
		case 1:
			require.NoError(t, compact.Set(ctx, triple, nil))
			require.NoError(t, memory.Set(ctx, triple, nil))
		default:
			require.NoError(t, compact.Add(ctx, triple, nil))
			require.NoError(t, memory.Add(ctx, triple, nil))
		}
		require.Equal(t, count(t, memory), count(t, compact), "step %d", step)
		for _, pattern := range patterns(randomTriple()) {
			require.Equal(t, triples(t, memory.Triples(ctx, pattern, nil)), triples(t, compact.Triples(ctx, pattern, nil)), "step %d pattern %v", step, pattern)
		}
	}
}
