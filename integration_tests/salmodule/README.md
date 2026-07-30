# SAL Module Integration Tests

This directory exercises SAL's docker round trip for SAL modules end to end:
SAL clones a module repository, builds the `Dockerfile` in its root, runs the
SAL Module command line interface inside the container, and merges the RDF the
module emits into a built Iceberg table.

## The fixture module

`module/` is a deliberately trivial SAL module. It is a busybox image wrapping
`salmodule.sh`, which implements the SAL Module command line interface with
hardcoded output:

- `salmodule ontology` prints a JSON-LD vocabulary declaring `StaticPlaceProducer`
  as a subclass of `salmodule:Task`, plus a `NotATask` class used to check that a
  non-task class is never run.
- `salmodule run` prints two `schema:Place` nodes, or a `salmodule:Error` node
  followed by a non-zero exit when the task instance contains `"fail":true`.

It is a test fixture rather than a reference implementation. See
[`examples/salmodule/python-geoconnex`](../../examples/salmodule/python-geoconnex)
for a module worth copying.

## Requirements

- A running docker daemon.
- `git` on `PATH`.
- Network access. Validation resolves the SAL Module vocabulary from
  `w3id.org`; the module itself is never fetched over the network.

## Run

From the repository root:

```sh
make integration_test_salmodule
```

or directly:

```sh
go test -tags integration -v -timeout 15m ./integration_tests/salmodule/...
```

The `integration` build tag keeps these tests out of `go test ./...` so that
building containers does not slow down local test driven development. CI runs
them through `.github/workflows/salmodule_integration.yml`.

## Notes

The module is committed to a throwaway git repository and SAL's clone step is
pointed at it, so the clone, the docker build, the container run, and the
Iceberg write are all real. Only the remote URL that SAL derives from the
`salmodule://` IRI is substituted; nothing else is faked.

Each test builds a fresh SAL project in a temporary directory, runs `sal init`
in it, and reads the resulting triples back out of the Iceberg table.
`TestModuleImplementsSalModuleCli` additionally runs the fixture in an ephemeral
testcontainers container, independently of SAL, to confirm the fixture itself
speaks the module interface.
