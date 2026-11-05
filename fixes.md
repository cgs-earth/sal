# What geoconnex-salproject can drop now that the sal fixes are in

This lists the special-case handling `geoconnex-salproject` carries today (in `job/serve-entrypoint.sh`, `job/entrypoint.sh`, the `Dockerfile`, `job/pins/`, and `terraform/README.md`) and what each piece becomes once it runs a sal build that includes the fixes for `issues.md`.

## What changed in sal

| Fix | Effect on the downstream project |
| --- | --- |
| `sal serve --data D` finds the table at any depth, in all three layouts (a copy of `.sal/data`, a bucket-root upload, an explicit-table-root upload). It finds `blobs/` and `stac/` beside the namespace directory. | No need to build a fake project around the copy or to work out the table's path by hand. |
| Every Iceberg read the UI makes (`iceberg_metadata`, `iceberg_column_stats`) now uses `allow_moved_paths = true`. | The storage-layout view works on an uploaded copy without credentials. `sal rewrite` is no longer needed. |
| `sal upload` copies `.sal/config.jsonld` to the top of the upload. `serve --data` reads it, plus `blobs/`, from the data directory. The Vocabularies tab, `/api/vocabularies` and the Stats title and description work with no `.sal/` and no git remote. | Nothing has to carry `config.jsonld` to the service by hand. |
| A data directory with no `config.jsonld` logs a warning saying pinned vocabularies are not listed. | The failure is visible instead of looking like "the project pins nothing". |
| `sal init` downloads 16 common vocabularies into `.sal/data/blobs`: rdf, rdfs, owl, dcterms, dc, schema.org, sosa, ssn, qudt, skos, foaf, prov, dcat, geosparql, time and shacl. They are seeded in `blobs/seed.jsonld` but **not pinned**. A build that uses one pins the seeded document without any network. `--no-cache` ignores seeds. `--no-vocabularies` skips the download. | Replaces the hand-maintained `job/pins/` directory. See step 4. |
| Vocabulary fetches retry (4 attempts, doubling backoff) on network errors, timeouts and 5xx or 429. An unreachable host gets an error that says how to seed the vocabulary. | One slow `purl.org` dial no longer fails a build outright. |

## 1. `job/serve-entrypoint.sh`

Keep the part that copies the bucket onto local disk. Cloud Run's filesystem is in memory and the service doesn't mount the bucket. Delete everything after the "copied N files" log line, and the `PROJECT_DIR` indirection.

Delete these:

- The `find ... -name metadata` table discovery, the `project_name` / `table` variables and the "no Iceberg table found" diagnostics. `sal serve` locates the table itself and refuses a directory with none or with several.
- `git init`, `git remote add origin https://github.com/cgs-earth/$project_name.git`, and the `.sal/` project built around the copy. This also removes the hard-coded org name in the script.
- The `mv "$DATA_DIR/config.jsonld" .sal/config.jsonld` step. `serve --data` reads `$DATA_DIR/config.jsonld` where it is.
- The fallback `cp /app/pins/config.jsonld .sal/config.jsonld` for images "older than that". An old bucket just lists only the built-in vocabularies and logs a warning until the next `make job-run`.
- `/app/sal rewrite --new-table-root "$table"` and its comment about the 403.

The tail of the script becomes:

```bash
exec /app/sal serve --data "$DATA_DIR" ${WITH_UI:+--with-ui}
```

with `DATA_DIR="${DATA_DIR:-/data}"`. The `rm -rf "$PROJECT_DIR"; mkdir -p "$DATA_DIR"` lines just use `$DATA_DIR`. `PROJECT_DIR` goes from the header comment too.

`Dockerfile`, `serve` target: delete `COPY job/pins/config.jsonld /app/pins/config.jsonld`. The service no longer reads baked pins.

Optional follow-up: the sal fix also makes the Terraform that `sal terraform` generates work (FUSE mount at `/mnt/sal` plus `sal serve --data /mnt/sal`) on a bucket-root upload. Before the fix it would have failed with "no Iceberg table metadata found". The download script exists because of in-memory-filesystem and cold-start concerns, not because of this bug, so keep it unless FUSE reads turn out fast enough. Measure that before deleting the script.

## 2. `job/entrypoint.sh`: upload

- Delete:
  ```bash
  cp .sal/config.jsonld .sal/data/config.jsonld
  ```
  and the comment above it, "sal upload sends only .sal/data...". `sal upload` stages `.sal/config.jsonld` itself.

## 3. `job/entrypoint.sh`: project creation

The repo already commits `.sal/config.jsonld` (the project node), `.gitignore` and `Makefile`, all of which `sal init` would write. The job's Docker image copies only `data/` into `/app/project`, so `sal init` runs there to recreate them. Beyond `.sal/data`, none of it matters to a build. Note that `sal init` refuses to run if `.sal/config.jsonld` already exists.

Pick one, and add `--no-vocabularies` if `sal init` stays:

- **Keep `sal init`** (the smallest change). Add `--no-vocabularies`, because the job runs on a network where `purl.org` is unreachable. Seeding there would only spend up to about 2 minutes of timeouts, and step 4 supplies the vocabularies another way:
  ```bash
  /app/sal init --no-skills --no-vocabularies --description "A representation of the Geoconnex graph as a SAL project"
  ```
- **Drop `sal init`.** `COPY .sal/config.jsonld` and the other project files into `/app/project`, and replace the call with `mkdir -p .sal/data`. Nothing else in `sal init` matters to a build. Only do this if the committed `.sal/config.jsonld` is what the job should build with, because the job overwrites it in step 4 today.

## 4. `job/pins/` and the "Vocabulary pins" procedure

Today `job/pins/` (a `config.jsonld` plus 5 blobs) is baked into the job image and copied over `.sal/` after `sal init`. The README's refresh procedure needs a privileged local `docker run` of the whole job, then copying `/work/.sal/...` out of a stopped container. All of it exists so the job never has to fetch `purl.org` from Cloud Run.

The seeding replaces that. Seed at **image build time**, where the network is Cloud Build's and not Cloud Run's:

```dockerfile
# in the job target, after sal is available
RUN set -e; d=$(mktemp -d); cd "$d"; \
    git init -q && git remote add origin https://github.com/cgs-earth/geoconnex-salproject.git; \
    /app/sal init --no-skills --description seed; \
    mkdir -p /app/seed && cp -R .sal/data/blobs/. /app/seed/
```

and in `job/entrypoint.sh`, in place of the `if [ -d /app/pins ]` block:

```bash
mkdir -p .sal/data/blobs
cp -R /app/seed/. .sal/data/blobs/      # seed.jsonld + the documents; nothing is pinned yet
```

Then `sal build` pins what the project actually uses (dcterms, geosparql, schema.org, salmodule, and the geoconnex module) from the seeds, with no vocabulary download for the 16 seeded namespaces. The `w3id.org/sal/salmodule#` and `salmodule://...` vocabularies are not in the common list. `w3id.org/sal/salmodule#` is still fetched, with retries. The `salmodule://` one is resolved by building the module, as before.

Delete after that:

- `job/pins/` and the `COPY job/pins/ /app/pins/` line in the job target.
- The `cp /app/pins/config.jsonld .sal/config.jsonld` that overwrites the project's config. This also stops the job from replacing the committed project node with a stale copy of it.
- The "Vocabulary pins" section of `terraform/README.md`, including the privileged-docker refresh recipe. Refreshing is now `make images`.
- The `curl` egress-check loop in `entrypoint.sh`. It is diagnostic only. Keep it if you still want egress visibility, but nothing depends on it.

Trade-off: with `job/pins/` the vocabulary versions were fixed in git. With seeds they are fixed per image build, and each build's `.sal/config.jsonld` is uploaded to the bucket, so which versions a given data product used is still recorded. If git-level reproducibility matters more, keep `job/pins/` and only drop the `rewrite`, git-project and config-carrying steps from sections 1 and 2.

## 5. What does not go away

- The Docker daemon, `runc-capfix` and the module-container plumbing. That is about running the harvester, not about sal's serve path.
- `INCLUDE_SITEMAPS` / `EXCLUDE_SITEMAPS`.
- The `SERVICE_URL` revision roll. A running instance still keeps the copy it made at start.
- The bucket download in `serve-entrypoint.sh`, for the in-memory reasons above.

## 6. Unexplained item from the issue: `geosparql` listed with 0 classes and 0 properties

The cause is the document, not the parser. `http://www.opengis.net/ont/geosparql` always 303-redirects, whatever `Accept` is sent, to an annotation-only profile (`Content-Type: text/anot+turtle`). It names 66 terms and gives each a `prez:label` and `prez:description`, but never an `rdf:type`. Validation is unaffected, because term membership is complete. The listing classifies terms by `rdf:type`, so it shows nothing under Classes or Properties and all 66 under Others.

I did not change this. A fix needs a trustworthy source for the full GeoSPARQL ontology, and the URLs I tried returned 404. Nothing in the geoconnex project needs to change for it.

## What was verified, and what was not

- **Verified:** a real Iceberg table, uploaded through the real `sal upload` code to a fake bucket root with `gs://my-bucket/`, then served with `sal serve --data --with-ui` from a directory with no `.sal/` and no git. SPARQL returned the right count, `/api/stats` returned the layout with no `layoutError`, `/api/vocabularies` listed the pinned vocabulary with its classes and properties, and `/stac/catalog.json` and `/blobs/<hash>` returned 200. The layout test fails with the original 403 when the `allow_moved_paths` fix is reverted. A live run of the seeder fetched and parsed all 16 vocabularies (2.4 MB).
- **Not verified:** the 1.25M-triple project on Cloud Run, the real `gs://` bucket, and a Cloud Storage FUSE mount. The `sal terraform` fix is argued from the shared locate code and the existing test of the generated arguments. Run the first `make job-run` and `make apply` after the change as the real check.
- **Not done:** the fallback that rebuilds the vocabulary listing from the table's `owl:Ontology` provenance nodes when no `config.jsonld` is available. Provenance nodes for module-copied files share the same shape, so reading them back needs a safe discriminator first. Uploading `config.jsonld` covers the case, and the missing-config warning covers the rest.
