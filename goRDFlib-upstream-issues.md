# goRDFlib issues to file

Four issues, each ready to paste. Context for all of them: I maintain [sal](https://github.com/cgs-earth/sal), which builds an RDF graph from a project's Turtle/JSON-LD plus the output of containerized tasks and writes it to Iceberg. One task produces tens of millions of triples, and a build of it hit 33 GB. Profiling that build turned up the four things below. sal was on v0.1.12 when I profiled; I re-checked each point against v0.9.1 and the numbers below say which version they come from. Go 1.26, linux/arm64.

This file is not part of sal; delete it once the issues are filed.

---

## Issue 1

**Title:** MemoryStore costs ~1.6 KB per triple because of singleton nested maps

I maintain sal, which builds graphs on goRDFlib and writes them to Iceberg. One of our builds materializes tens of millions of triples from a module task and ran out of memory at 33 GB. Profiling it, almost all of the retained heap is `store.MemoryStore`.

`MemoryStore` keeps three nested maps per graph (`spo`, `pos`, `osp`, each `map[string]map[string]map[string]*term.Triple` in v0.9.1). In instance data most objects are literals that occur once, so the inner maps of `pos` and `osp` are nearly all singletons. Since Go 1.24 a map allocates an 8-slot group up front, so a singleton inner map costs a few hundred bytes before it holds anything, and a triple with a unique object creates three or four of them. v0.9.1's switch from a `Triple` value to a `*Triple` at the leaves helped (see below) but the singleton maps are still most of the cost.

**What I measured.** One million triples `<s/i> schema:name "name i"` with distinct subjects and literals, then `runtime.GC()`:

| version | live heap |
|---|---|
| v0.1.12 | 2,998 MB |
| v0.9.1 | 1,640 MB |
| interning store with postings lists (below), on v0.1.12 | 567 MB, most of it the 2M distinct terms themselves |

On a richer 1.6M-triple JSON-LD document (names, descriptions, nested blank nodes, IRI objects) v0.1.12 held 3,790 MB and the interning store 310 MB.

**Reproduce (v0.9.1 API):**

```go
ctx := context.Background()
g := rdflibgo.NewGraph()
p := rdflibgo.NewURIRefUnsafe("https://schema.org/name")
for i := 0; i < 1_000_000; i++ {
	s := rdflibgo.NewURIRefUnsafe(fmt.Sprintf("https://example.test/s/%d", i))
	_ = g.Add(ctx, s, p, rdflibgo.NewLiteral(fmt.Sprintf("name %d", i)))
}
runtime.GC()
var ms runtime.MemStats
runtime.ReadMemStats(&ms)
fmt.Println(ms.HeapAlloc>>20, "MB") // ~1640 MB on v0.9.1
runtime.KeepAlive(g)
```

**What I'd suggest:**

- Intern terms into a dictionary (`[]term.Term` plus `map[string]uint32` by `TermKey`) and store a triple as three `uint32` ids, 12 bytes instead of a `Triple` plus three pointer slots.
- Replace the two inner map levels with a postings list per term (`map[uint32][]int32` of triple slots) and answer a pattern by filtering the shortest list the bound terms allow. A single `map[[3]uint32]int32` gives the existence check and removal.
- If the nested-map layout has to stay, make the innermost level a small slice until it passes roughly 8 entries, which is where a Go map starts paying for itself.

I have a working `store.Store` + `store.QueryableStore` implementation of the first two points in sal (`pkg/compactstore`, [cgs-earth/sal](https://github.com/cgs-earth/sal)), with a randomized differential test against `MemoryStore` covering every pattern shape, `Remove`, and `Set`. It is written against the v0.1.x interface and would need the v0.9 `ctx`/`error` signatures and a `storetest.Run` pass, but I'm happy to upstream it, either as a second store or as a replacement for `MemoryStore`'s internals, if you'd take it.

---

## Issue 2

**Title:** jsonld: no way to parse a large document without expanding it whole

v0.9.1 already fixed two things I was going to report here: `Parse` converts json-gold's `RDFDataset` directly instead of round-tripping through an N-Quads string, and blank nodes get a fresh scope per `Parse` call so batching documents into one graph no longer merges unrelated blank nodes. Thanks for both. What remains is that there is no way to parse a document incrementally. `Parse` and `ParseStream` both decode the entire document into `any` and expand it whole before the first triple is produced, and `ParseStream`'s own doc says so.

In sal, a containerized task writes newline-delimited JSON nodes that all share one `@context`, sometimes tens of millions of triples' worth. I used to wrap them all in one `@graph` and call `Parse` once, which needed several multiples of the output size in memory. I now batch 1,000 nodes per `Parse` call myself, which works, but every batch pays to process the same `@context` again and I have to manage batching and error line numbers outside the library.

**What I'd suggest:** a streaming entry point for the "one context, many node objects" shape, for example `jsonld.NewStreamParser(ctx, g, context, opts...)` with `ParseNode(json.RawMessage) error` and `Flush() error`, or a `ParseNodes(ctx, g, context, iter.Seq[json.RawMessage], opts...)`. The context would be processed once, each node (or small batch) expanded on its own with that active context, and triples added as they arrive, with the blank node scope either per call or per parser as an option. json-gold can expand a single node object given a pre-processed context, so I think this can be built from what it already exposes.

---

## Issue 3

**Title:** term.TermKey allocates and re-serializes a Literal on every call

Still the case in v0.9.1. `URIRef` and `BNode` cache their key in the struct, but `TermKey(Literal)` builds `"L:" + v.N3()` every time it is called. `MemoryStore`'s add, remove, and every pattern lookup compute the key for each term, so every store operation involving a literal allocates and serializes it. For a large literal, such as a multi-megabyte WKT polygon, that copies the whole value on each add, remove, and lookup.

Caching the key on `Literal` at construction, the way `URIRef` and `BNode` already do, would remove the allocation with no API change. `NewLiteral` already knows the lexical form, datatype, and language it needs.

---

## Issue 4

**Title:** Parsers allocate a fresh URIRef for every occurrence of a repeated IRI

Still the case in v0.9.1 as far as I can see. The N-Quads, Turtle, and JSON-LD parsers create a new `URIRef` for each occurrence of an IRI, and each `URIRef` carries two strings, `value` and the cached `"U:" + value` key. A predicate that occurs ten million times in a document is therefore allocated ten million times, twice. The same goes for the datatype IRI on every typed literal.

A small per-parse intern table for IRIs (a `map[string]URIRef` that lives for the duration of one `Parse` call) would return the same `URIRef` for repeated IRIs, cutting both allocation rate and the memory any store retains for the terms, with no API change. It would also help Issue 1, since a dictionary-based store would then see shared strings rather than one copy per triple.

---

## Suggested order

Issue 1 is the one that decides whether a large graph fits in memory at all; Issue 2 decides whether it can be parsed without holding the whole document; Issues 3 and 4 are smaller follow-ups from the same profile.
