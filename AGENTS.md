# SAL

SAL, (semantic accessibility layer), is a CLI tool for creating RDF data and metadata from a dataset. It contains a series of subcommands.

## Repository Structure

- sal is setup as a monorepo
    - `docs/` contains a documentation site written with Astro and Starlight on the overall project, any specifications, and sal commands at a high level
    - `examples/` contains examples of how to use sal or example sal module implementations
    - `.github/workflows` contains github workflows for CI/CD
    - Each sal subcommand is present in a folder of the same name. For instance -> `sal build` is present in the `build/` folder.
    - Any broadly reusable code is present in the `pkg` folder

## Subcommands

### `build`

- Takes in RDF data, validates it, and writes it to a local iceberg table in the sal data directory.
    - All input RDF data should be validated. This RDF data can be defined in either Turtle or JSON-LD. 
    - For all input RDF data, if there is a term that is not defined in the provided prefixes, SAL will throw an error which calls out the specific line number with the offending term.
        - For instance, if the user makes a typo and specifies `schema:nameee` in their JSON-LD, SAL build will throw an error saying that `nameee` is not a defined term in the RDF vocab. This should be supported for any generalized RDF vocabulary.
    - Only new data should be written to the iceberg table. For instance, if the table already has a particular triple, it should be ommitted from the new commit. 
        - An identifier for each triple can be calculated by hashing the subject, predicate, and object. This hash identifier can then be looked up in the iceberg table to check if it exists.
    - Build can also materialize triples from a sal module and add it to the iceberg table.
        - A sal module is another github repository with a Dockerfile in the root. 
            - More info on the sal module spec can be found here: @./docs/src/content/docs/reference/salmodule-description.mdx
            - An example sal module can be found here: @./examples/salmodule/python-geoconnex
        - A sal module is referred to in an rdf file by using the sal module ontology here: `https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#`
        - If a new term is defined in the sal module that is a subclass of `salmodule:NodeProcessor` the sal module should be built by fetching the remote git repository, building the docker container, passing the `salmodule:taskInstanceEnvVar` to the container. The JSON-LD that results from this process should be added to the in memory goRDFlib graph and thus added to the iceberg table upon a successful build. 

### `clone`

- Takes in a full URL to an OCI artifact. 
- It uses the oras golang to introspect the manifest info about the artifact. It retrieves the value of the `org.opencontainers.image.source` to determine the location of the source code. It finds the pinned commit hash using the value of `sal.git-commit-hash`.
    - It then clones the source code into the current working directory.
    - It checks out the commit hash specified in the manifest.
    - Within the newly cloned repo directory, it should run `sal init` to initialize a sal project.
    - Within the new cloned repo directory, it should pull the OCI artifact and place it in the `.sal/data` directory.

### `edit`

- Rewrites Iceberg table metadata so a built table can be read from a new table root.
- `edit.RewriteIcebergTableRoot` must update every Iceberg metadata file that can contain paths: `*.metadata.json`, manifest list Avro files, and manifest Avro files.
    - JSON metadata contains fields such as `location`, `manifest-list`, and `metadata-file`.
    - Avro metadata contains fields such as `manifest_path` and data file `file_path`.
- The edit command must not rewrite Parquet data files. It only changes metadata references.
- Path rewriting is prefix based: the old table root is replaced by the new table root, and child paths under the old root are preserved.
- `edit.RewriteIcebergMetadataPath` exists for upload-time metadata renames. Use it when a single metadata object name changes and every reference to that exact metadata path must be updated.

### `upload`

- upload copies `.sal/data` to a temporary staging directory, rewrites the staged Iceberg metadata, and uploads the staged files. The local `.sal/data` directory must remain unchanged.
- upload uses the edit package for Iceberg metadata rewrites. Do not duplicate JSON or Avro metadata traversal logic in upload unless the edit package cannot represent the operation.
- Bucket URL semantics are important:
    - A bare object-store bucket like `gs://my-bucket/` uploads the full `.sal/data` layout. If the local table is `.sal/data/sal/triples`, the uploaded table root is `gs://my-bucket/sal/triples`.
    - An explicit object-store path or prefix like `gs://my-bucket/sal/triples` or `gs://my-bucket?prefix=sal/triples/` is treated as the table root when there is exactly one Iceberg table.
    - `https://storage.googleapis.com/<bucket>/...` is a supported GCS-compatible URL form. upload must upload through the GCS blob driver but write `https://storage.googleapis.com/<bucket>/...` paths into Iceberg metadata so S3-compatible tooling can resolve the table through HTTPS.
    - Multiple Iceberg tables always preserve their relative table paths under the bucket root or prefix.
- Upload ordering matters for readers: data files first, metadata files second, and `metadata/version-hint.text` last.
- Object-store uploads (`gs`, `s3`, `azblob`, and `https://storage.googleapis.com`) must use fresh metadata object names during upload. Reusing the same Avro manifest object names can leave DuckDB or HTTP/object-store caches reading stale manifests after a reupload.
    - Fresh upload metadata includes renamed Avro metadata files, updated references to those renamed files, a new `v<number>.metadata.json`, and a `version-hint.text` pointing to that version.
    - `version-hint.text` must not include a trailing newline; DuckDB treats the newline as part of the version string.
- Percent escaping depends on the table root scheme:
    - Do not percent-escape Iceberg `file_path` values for `gs://`, `s3://`, or `azblob://` metadata roots. The object-store client handles URL encoding when fetching the object.
    - Do percent-escape child path suffixes for literal `http://` or `https://` metadata roots. Local partition directory names may contain literal percent-encoded values like `predicate_partition=http%3A%2F%2Fschema.org%2Fte`; in an HTTPS URL these percent signs must become `%25` so the remote object key resolves correctly.
- The key upload verification query is:

```sql
SET enable_object_cache = false;
SELECT
    subject,
    predicate,
    object
FROM iceberg_scan(
    'gs://sal-test-bucket/sal/triples'
)
LIMIT 5;
```


### `serve` 

- Starts a local HTTP server on port 8080.
- The server can resolve SPARQL queries against the iceberg triples table

## Code Style

- Use testify for writing succinct tests; avoid just the standard library and if statements with `t.Error()`.
- If a function would only be called from one other function and it is short, try to just condense it into the function that calls it.
- Any function with functionality that is non trivial should be documented with a succinct comment of what it does.
- Don't create functions that are very small and only used in a single place.
- Prefer to not use table oriented tests unless there is significant test duplication; make each test case a separate test function so it is easier to debug and isolate failures.
- All tests should be unit tests without external fetches to remote servers. All local filesystem operations should be mocked or written to tmp files. 

## Boundaries

* Ask First:
    - Large cross-package refactors.
    - If some functionality would be very complex, duplicative, and better handled by an underlying library like json-gold or goRDFlib, explicitly call it out; don't try to create a custom fix here with lots of specialized code
    - If the AGENTS.md file should be updated to reflect new changes, explicitly call it out

## Development 

- `go test ./...` should pass after every new feature
- For any major new user facing features, add associated documentation in the `/docs` folder