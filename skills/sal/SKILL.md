---
name: sal
description: Use the SAL (semantic accessibility layer) CLI to create, validate, build, run, query, and publish a SAL project, a git repository of Turtle or JSON-LD that sal turns into an Iceberg triples table. Use when a task mentions sal, a .sal directory, .sal/config.jsonld, RDF validation against vocabularies, salmodule:// prefixes in project RDF, or querying a built triples table with SQL or SPARQL.
license: Apache-2.0
metadata:
  homepage: https://cgs-earth.github.io/sal/
  source: https://github.com/cgs-earth/sal
---

# SAL projects

SAL builds a data product from RDF. A project is an ordinary git repository whose Turtle or JSON-LD
files are validated against the vocabularies they declare and committed to a local Apache Iceberg
table under `.sal/data`. Everything generated lives in `.sal/data` and is gitignored; the one generated
file that is committed is `.sal/config.jsonld`, the lockfile of pinned vocabulary versions and imports.

## Prerequisites

- `sal` on `PATH`: `go install -a github.com/cgs-earth/sal@latest` (needs cgo and a C toolchain, since DuckDB is linked in).
- `git`, and a repository with a remote. `sal init` refuses a repo without one.
- A docker daemon only if the project references SAL modules.

## Lifecycle

```sh
sal init                 # once, at the repo root; creates .sal/data and gitignores it
sal validate data/       # parse and check every term against its vocabulary; commits nothing
git add -A && git commit # build refuses a dirty worktree
sal build data/          # validate, then commit new triples to .sal/data as an Iceberg snapshot
sal run                  # only if the RDF declares SAL module tasks; materializes their output
sal query                # SQL shell over the `triples` view; `sal query --sparql` for SPARQL
sal serve --with-ui      # http://localhost:8080 with SPARQL, SQL, map, and stats tabs
```

`sal build` and `sal run` refuse to run on an uncommitted worktree so a snapshot always maps to a
commit. `--force` skips that check for debugging only. After a build that pinned new vocabularies,
commit `.sal/config.jsonld` before the next build.

## Writing RDF that validates

- Every prefixed term is checked against the vocabulary its prefix names, so `schema:nameee` is an
  error reported as `file:line: undefined term`. Fix the term, not the prefix.
- Prefix namespaces must end in `/` or `#`. One ending in neither needs confirmation or
  `--allow-prefixes-without-slash-or-hash`. One vocabulary declared with mixed spellings
  (`http` vs `https`, `/` vs `#`) across files is an error; reconcile with `--prefix-maps old=new`.
- Relative IRIs resolve against the project base, derived from the git remote. Use them for the
  project's own instances and absolute IRIs for everything external.
- Only new triples are committed; a rebuild after no change adds nothing.
- Vocabularies are pinned in `.sal/config.jsonld` by SHA-256 on first use, and later builds validate
  against the pinned copy. `sal build --no-cache` re-resolves and re-pins everything.

## Imports

Pinning validates against a vocabulary; importing puts its statements in the table.

```sh
sal import https://www.w3.org/ns/sosa/                      # ontology over HTTP
sal import salmodule://github.com/owner/module               # a SAL module's ontology
sal import oci://ghcr.io/org/other-data-product:latest       # another data product, pulled to .sal/data/imports
```

Imports are recorded on the project ontology node in `.sal/config.jsonld` and fetched by `sal build`.

## Using a SAL module from a project

A SAL module is a git repository with a Dockerfile that produces triples on demand. Reference it with
a `salmodule://[HOST/]OWNER/REPO/` prefix, which must end in a slash, and type an instance with a
class from the module's ontology plus a task base class:

```turtle
@prefix salmodule: <https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#> .
@prefix states: <salmodule://github.com/cgs-earth/python-geoconnex/> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

<GeoconnexStates> a states:GeoconnexReferenceFeatureStates, salmodule:NodeProcessor ;
    states:maxRetries "5"^^xsd:integer .
```

- Configure the task only with properties the module's own vocabulary defines; anything else on the
  instance is ignored when the task instance is serialized.
- `sal validate` and `sal build` clone and build the module to check its terms, but never run it.
  `sal run` runs each task and commits what it emits as a new snapshot. Files a task hands over land in
  `.sal/data/blobs` named by SHA-256 and appear in the graph as `urn:sha256:<digest>`.
- Modules are pinned by git commit in `.sal/config.jsonld`; `sal build --no-cache` picks up a new commit.
- `sal salmodule inspect salmodule://owner/repo` prints a module's ontology without a project.
- To write a module, use the `salmodule` skill.

## Querying

- `sal query` opens SQL over a `triples` view with columns `subject`, `predicate`, `object`,
  `object_type`, `object_language`, `object_geometry`. `sal query --info snapshots` lists snapshots.
- `sal query --sparql` translates SPARQL to SQL; `sal serve` exposes `/sparql` and `/v{snapshot}/sparql`.
- `sal get classes|properties|instances|statements|vocabularies` and `sal describe <iri>` answer the
  common questions without writing a query.
- GeoSPARQL `geof:` filters and WKT/GeoJSON objects are supported; the UI's Map tab plots results.

## Publishing and consuming

```sh
sal push ghcr.io/org/product --username "$U" --password "$P"   # OCI artifact tagged latest and by commit
sal clone ghcr.io/org/product:latest                           # clone source at that commit and restore .sal/data
sal upload gs://bucket/                                        # copy the Iceberg table to an object store
sal clean --wipe                                               # remove .sal/data and .sal/config.jsonld
```

## Troubleshooting

- `undefined term` for a module class: the `salmodule://` prefix is missing its trailing slash.
- Build refuses to run: commit, or `--force` for a throwaway run.
- A table built by an older sal is refused: `sal clean --wipe` then rebuild.
- A module's task changed but its output did not: `sal build --no-cache` to drop the commit pin.
