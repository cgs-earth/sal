package pkg

import (
	"context"
	"fmt"
	"iter"

	rdflibgo "github.com/tggo/goRDFlib"
)

// Every graph sal builds lives in memory, on goRDFlib's MemoryStore or on
// pkg/compactstore, and neither can fail a read or a write. goRDFlib v0.9
// nevertheless made every graph method take a context and return an error so
// that a persistent store can report one, which for sal would mean every
// callback and every helper threading an error that cannot happen. The
// functions below call the graph with a background context and treat an error
// as what it is for an in-memory store, a bug, by panicking with it. Parsers
// and serializers, which do real I/O, are called directly and their errors
// handled.

// Triples returns the triples of g matching the pattern, nil meaning any.
func Triples(g *rdflibgo.Graph, s rdflibgo.Subject, p *rdflibgo.URIRef, o rdflibgo.Term) iter.Seq[rdflibgo.Triple] {
	return func(yield func(rdflibgo.Triple) bool) {
		for triple, err := range g.Triples(context.Background(), s, p, o) {
			if err != nil {
				panic(fmt.Errorf("read in-memory graph: %w", err))
			}
			if !yield(triple) {
				return
			}
		}
	}
}

// AddTriple adds one triple to g.
func AddTriple(g *rdflibgo.Graph, s rdflibgo.Subject, p rdflibgo.URIRef, o rdflibgo.Term) {
	if err := g.Add(context.Background(), s, p, o); err != nil {
		panic(fmt.Errorf("add to in-memory graph: %w", err))
	}
}

// SetTriple replaces every value of s and p in g with o.
func SetTriple(g *rdflibgo.Graph, s rdflibgo.Subject, p rdflibgo.URIRef, o rdflibgo.Term) {
	if err := g.Set(context.Background(), s, p, o); err != nil {
		panic(fmt.Errorf("set in in-memory graph: %w", err))
	}
}

// RemoveTriples removes every triple of g matching the pattern, nil meaning any.
func RemoveTriples(g *rdflibgo.Graph, s rdflibgo.Subject, p *rdflibgo.URIRef, o rdflibgo.Term) {
	if err := g.Remove(context.Background(), s, p, o); err != nil {
		panic(fmt.Errorf("remove from in-memory graph: %w", err))
	}
}

// GraphLen returns the number of triples in g.
func GraphLen(g *rdflibgo.Graph) int {
	n, err := g.Len(context.Background())
	if err != nil {
		panic(fmt.Errorf("count in-memory graph: %w", err))
	}
	return n
}

// Contains reports whether g holds the triple.
func Contains(g *rdflibgo.Graph, s rdflibgo.Subject, p rdflibgo.URIRef, o rdflibgo.Term) bool {
	found, err := g.Contains(context.Background(), s, p, o)
	if err != nil {
		panic(fmt.Errorf("read in-memory graph: %w", err))
	}
	return found
}

// Bind binds a prefix to a namespace in g.
func Bind(g *rdflibgo.Graph, prefix string, namespace rdflibgo.URIRef) {
	if err := g.Bind(context.Background(), prefix, namespace); err != nil {
		panic(fmt.Errorf("bind prefix in in-memory graph: %w", err))
	}
}

// Namespaces returns the prefix bindings of g.
func Namespaces(g *rdflibgo.Graph) iter.Seq2[string, rdflibgo.URIRef] {
	return func(yield func(string, rdflibgo.URIRef) bool) {
		for binding, err := range g.Namespaces(context.Background()) {
			if err != nil {
				panic(fmt.Errorf("read namespaces of in-memory graph: %w", err))
			}
			if !yield(binding.Prefix, binding.Namespace) {
				return
			}
		}
	}
}

// ListItems returns the items of the RDF list in g whose head is head, in order.
func ListItems(g *rdflibgo.Graph, head rdflibgo.Subject) iter.Seq[rdflibgo.Term] {
	return func(yield func(rdflibgo.Term) bool) {
		for item, err := range rdflibgo.NewCollection(g, head).Iter(context.Background()) {
			if err != nil {
				panic(fmt.Errorf("read list in in-memory graph: %w", err))
			}
			if !yield(item) {
				return
			}
		}
	}
}
