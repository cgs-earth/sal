// Package compactstore is an in-memory goRDFlib store that holds a large graph
// in a fraction of the memory goRDFlib's own MemoryStore does.
//
// MemoryStore keeps three nested maps (subject → predicate → object, and the
// two rotations) with the triple at every leaf. The inner maps are almost all
// singletons, since most objects are literals that appear once, and a Go map
// costs several hundred bytes however few entries it holds, so a triple costs
// well over a kilobyte there. This store interns every distinct term once,
// keeps each triple as three 32-bit ids, and indexes them with postings lists,
// so a triple costs a few dozen bytes plus its share of the terms. It is what
// `sal build` holds the graph it commits in; a module task that materializes
// tens of millions of triples is only feasible with it.
//
// The store holds one unnamed graph and never fails on its own: the only
// error it returns is that of a context that is already done when a call
// starts, which the store.Store contract requires.
package compactstore

import (
	"context"
	"math"
	"sync"

	"github.com/tggo/goRDFlib/store"
	"github.com/tggo/goRDFlib/term"
)

// tombstone marks a removed triple's slot. The slot stays in the postings
// lists until the next compaction and is skipped on every read.
const tombstone = math.MaxUint32

// compactionMinimum is how many removed slots a store tolerates before it
// considers compacting; below it the garbage is not worth a rebuild.
const compactionMinimum = 4096

type triple struct{ s, p, o uint32 }

// Store implements store.Store and store.QueryableStore for a single
// unnamed graph. It is safe for concurrent use the way MemoryStore is: an
// iteration holds the read lock until it ends, so a store must not be
// mutated from inside one.
type Store struct {
	mu sync.RWMutex

	// terms is the dictionary: a term's id is its index here, and ids finds
	// an id by the term's TermKey
	terms []term.Term
	ids   map[string]uint32

	// triples holds every triple in insertion order; a removed one is
	// tombstoned in place. index maps a live triple to its slot.
	triples []triple
	index   map[triple]int32
	removed int

	// postings are the slots of the triples each term appears in, by
	// position; a tombstoned slot stays listed until compaction
	bySubject   map[uint32][]int32
	byPredicate map[uint32][]int32
	byObject    map[uint32][]int32

	nsPrefix map[string]term.URIRef
	nsURI    map[string]string
}

// New returns an empty store.
func New() *Store {
	return &Store{
		ids:         map[string]uint32{},
		index:       map[triple]int32{},
		bySubject:   map[uint32][]int32{},
		byPredicate: map[uint32][]int32{},
		byObject:    map[uint32][]int32{},
		nsPrefix:    map[string]term.URIRef{},
		nsURI:       map[string]string{},
	}
}

// intern returns the id of t, adding it to the dictionary if it is new.
func (s *Store) intern(t term.Term) uint32 {
	key := term.TermKey(t)
	if id, ok := s.ids[key]; ok {
		return id
	}
	id := uint32(len(s.terms))
	s.terms = append(s.terms, t)
	s.ids[key] = id
	return id
}

// lookup returns the id of t and whether the dictionary has it; a pattern
// naming a term the store has never seen matches nothing.
func (s *Store) lookup(t term.Term) (uint32, bool) {
	id, ok := s.ids[term.TermKey(t)]
	return id, ok
}

func (s *Store) Add(ctx context.Context, t term.Triple, _ term.Term) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addLocked(t)
	return nil
}

func (s *Store) addLocked(t term.Triple) {
	key := triple{s.intern(t.Subject), s.intern(t.Predicate), s.intern(t.Object)}
	if _, exists := s.index[key]; exists {
		return
	}
	slot := int32(len(s.triples))
	s.triples = append(s.triples, key)
	s.index[key] = slot
	s.bySubject[key.s] = append(s.bySubject[key.s], slot)
	s.byPredicate[key.p] = append(s.byPredicate[key.p], slot)
	s.byObject[key.o] = append(s.byObject[key.o], slot)
}

func (s *Store) AddN(ctx context.Context, quads []term.Quad) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, quad := range quads {
		s.addLocked(quad.Triple)
	}
	return nil
}

func (s *Store) Remove(ctx context.Context, pattern term.TriplePattern, _ term.Term) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var slots []int32
	s.matchLocked(pattern, func(slot int32) bool {
		slots = append(slots, slot)
		return true
	})
	for _, slot := range slots {
		s.removeLocked(slot)
	}
	s.maybeCompactLocked()
	return nil
}

// removeLocked tombstones one slot. The slot stays in the postings lists; a
// read skips it and a compaction drops it.
func (s *Store) removeLocked(slot int32) {
	delete(s.index, s.triples[slot])
	s.triples[slot] = triple{tombstone, tombstone, tombstone}
	s.removed++
}

// maybeCompactLocked rebuilds the triples and postings without the
// tombstones once they outnumber the live triples, so that a store that is
// rewritten in place, the way blank node stabilization rewrites one, does not
// keep paying for what it removed.
func (s *Store) maybeCompactLocked() {
	if s.removed < compactionMinimum || s.removed < len(s.triples)/2 {
		return
	}
	live := make([]triple, 0, len(s.triples)-s.removed)
	for _, t := range s.triples {
		if t.s != tombstone {
			live = append(live, t)
		}
	}
	s.triples = live
	s.removed = 0
	s.index = make(map[triple]int32, len(live))
	s.bySubject = make(map[uint32][]int32, len(s.bySubject))
	s.byPredicate = make(map[uint32][]int32, len(s.byPredicate))
	s.byObject = make(map[uint32][]int32, len(s.byObject))
	for slot, t := range live {
		s.index[t] = int32(slot)
		s.bySubject[t.s] = append(s.bySubject[t.s], int32(slot))
		s.byPredicate[t.p] = append(s.byPredicate[t.p], int32(slot))
		s.byObject[t.o] = append(s.byObject[t.o], int32(slot))
	}
}

func (s *Store) Set(ctx context.Context, t term.Triple, _ term.Term) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var slots []int32
	s.matchLocked(term.TriplePattern{Subject: t.Subject, Predicate: &t.Predicate}, func(slot int32) bool {
		slots = append(slots, slot)
		return true
	})
	for _, slot := range slots {
		s.removeLocked(slot)
	}
	s.addLocked(t)
	s.maybeCompactLocked()
	return nil
}

// matchLocked calls visit with the slot of every live triple matching the
// pattern, reading the shortest postings list the bound terms allow: a
// subject's, else an object's, else a predicate's, else every triple in
// insertion order.
func (s *Store) matchLocked(pattern term.TriplePattern, visit func(slot int32) bool) {
	var want triple
	var ok bool
	hasS, hasP, hasO := pattern.Subject != nil, pattern.Predicate != nil, pattern.Object != nil
	if hasS {
		if want.s, ok = s.lookup(pattern.Subject); !ok {
			return
		}
	}
	if hasP {
		if want.p, ok = s.lookup(*pattern.Predicate); !ok {
			return
		}
	}
	if hasO {
		if want.o, ok = s.lookup(pattern.Object); !ok {
			return
		}
	}

	if hasS && hasP && hasO {
		if slot, exists := s.index[want]; exists {
			visit(slot)
		}
		return
	}

	var candidates []int32
	switch {
	case hasS:
		candidates = s.bySubject[want.s]
	case hasO:
		candidates = s.byObject[want.o]
	case hasP:
		candidates = s.byPredicate[want.p]
	default:
		for slot, t := range s.triples {
			if t.s != tombstone && !visit(int32(slot)) {
				return
			}
		}
		return
	}
	for _, slot := range candidates {
		t := s.triples[slot]
		if t.s == tombstone || (hasP && t.p != want.p) || (hasO && t.o != want.o) || (hasS && t.s != want.s) {
			continue
		}
		if !visit(slot) {
			return
		}
	}
}

// tripleAt rebuilds the triple in a slot from the dictionary.
func (s *Store) tripleAt(slot int32) term.Triple {
	t := s.triples[slot]
	return term.Triple{
		Subject:   s.terms[t.s].(term.Subject),
		Predicate: s.terms[t.p].(term.URIRef),
		Object:    s.terms[t.o],
	}
}

// Triples yields the matching triples; it yields ctx's error once and stops
// if the context is done.
func (s *Store) Triples(ctx context.Context, pattern term.TriplePattern, _ term.Term) store.TripleIterator {
	return s.TriplesWithLimit(ctx, pattern, nil, 0, 0)
}

func (s *Store) TriplesWithLimit(ctx context.Context, pattern term.TriplePattern, _ term.Term, limit, offset int) store.TripleIterator {
	return func(yield func(term.Triple, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(term.Triple{}, err)
			return
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		skipped, yielded := 0, 0
		s.matchLocked(pattern, func(slot int32) bool {
			if skipped < offset {
				skipped++
				return true
			}
			if limit > 0 && yielded >= limit {
				return false
			}
			yielded++
			return yield(s.tripleAt(slot), nil)
		})
	}
}

func (s *Store) Count(ctx context.Context, pattern term.TriplePattern, _ term.Term) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	s.matchLocked(pattern, func(int32) bool {
		n++
		return true
	})
	return n, nil
}

func (s *Store) Exists(ctx context.Context, pattern term.TriplePattern, _ term.Term) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := false
	s.matchLocked(pattern, func(int32) bool {
		found = true
		return false
	})
	return found, nil
}

func (s *Store) Len(ctx context.Context, _ term.Term) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.triples) - s.removed, nil
}

// Contexts yields nothing: the store holds one unnamed graph.
func (s *Store) Contexts(ctx context.Context, _ *term.Triple) store.TermIterator {
	return func(yield func(term.Term, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
		}
	}
}

func (s *Store) Bind(ctx context.Context, prefix string, namespace term.URIRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nsPrefix[prefix] = namespace
	s.nsURI[namespace.Value()] = prefix
	return nil
}

func (s *Store) Namespace(ctx context.Context, prefix string) (term.URIRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return term.URIRef{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespace, ok := s.nsPrefix[prefix]
	return namespace, ok, nil
}

func (s *Store) Prefix(ctx context.Context, namespace term.URIRef) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix, ok := s.nsURI[namespace.Value()]
	return prefix, ok, nil
}

func (s *Store) Namespaces(ctx context.Context) store.NamespaceIterator {
	return func(yield func(store.NamespaceBinding, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(store.NamespaceBinding{}, err)
			return
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		for prefix, namespace := range s.nsPrefix {
			if !yield(store.NamespaceBinding{Prefix: prefix, Namespace: namespace}, nil) {
				return
			}
		}
	}
}

func (s *Store) ContextAware() bool     { return false }
func (s *Store) TransactionAware() bool { return false }

var (
	_ store.Store          = (*Store)(nil)
	_ store.QueryableStore = (*Store)(nil)
)
