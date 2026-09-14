---
name: salmodule
description: Create, test, and publish a SAL module, a git repository with a Dockerfile whose container implements the SAL Module CLI (salmodule ontology, salmodule run) so that a SAL project can reference it with a salmodule:// prefix and sal run can materialize the triples and files it produces. Use when asked to write a sal module, implement salmodule ontology or run, emit JSON-LD from a container for sal, or debug why sal cannot resolve or run a module.
license: Apache-2.0
metadata:
  homepage: https://cgs-earth.github.io/sal/reference/salmodule-description/
  source: https://github.com/cgs-earth/sal
  example: https://github.com/cgs-earth/sal/tree/main/examples/salmodule/python-geoconnex
---

# SAL modules

A SAL module is a git repository with a `Dockerfile` in its root. The image it builds is a CLI with
two subcommands. `sal` clones the repository, builds the image, and runs those subcommands; nothing
else about the language, framework, or base image matters.

```sh
docker run IMAGE salmodule ontology                                   # print the module's vocabulary
docker run -e SALMODULE_TASK_INSTANCE='{...}' IMAGE salmodule run     # run one task instance
```

A project references the module as `salmodule://[HOST/]OWNER/REPO/` (host defaults to `github.com`),
types an instance with one of the module's task classes, and configures it with the module's own
properties. `sal validate` and `sal build` only ask for the ontology; `sal run` runs tasks.

## 1. The ontology command

Print one JSON-LD document to stdout and exit 0. Requirements:

- A `@context` that binds every prefix the module uses. It is injected into the task output later, so
  any key a task emits must be resolvable through it.
- A node `{"@id": ".", "@type": "owl:Ontology"}` describing the module.
- The module's own terms as relative IRIs (`"@id": "MyTask"`, `"@id": "maxRetries"`). They resolve
  against the project's `salmodule://.../` prefix, which is why that prefix must end in a slash.
- At least one task class: an `owl:Class` with `rdfs:subClassOf` one of `salmodule:Task`,
  `salmodule:NodeProcessor`, `salmodule:NodeProducer`, or `salmodule:NodeConsumer`.
- Every configuration property as an `owl:DatatypeProperty` or `owl:ObjectProperty`, ideally with
  `rdfs:domain` and `rdfs:range`. A property the ontology does not declare never reaches the task.
- Optional SHACL shapes on the task class: `salmodule:self` (what a valid instance looks like),
  `salmodule:input`, and `salmodule:output` (what the task consumes and produces).
- Optional `salmodule:taskInstanceEnvVar` on the ontology node to rename the environment variable;
  it defaults to `SALMODULE_TASK_INSTANCE`.

```json
{
  "@context": {
    "schema": "https://schema.org/",
    "owl": "http://www.w3.org/2002/07/owl#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "xsd": "http://www.w3.org/2001/XMLSchema#",
    "salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
  },
  "@graph": [
    {"@id": ".", "@type": "owl:Ontology", "rdfs:label": "Weather stations"},
    {"@id": "StationFetcher", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"},
     "rdfs:comment": "Fetches every station in a region and emits it as schema:Place"},
    {"@id": "region", "@type": "owl:DatatypeProperty",
     "rdfs:domain": {"@id": "StationFetcher"}, "rdfs:range": {"@id": "xsd:string"}}
  ]
}
```

## 2. The run command

Read the task instance from the environment variable as a JSON-LD node object. `sal` serializes it
from the project's RDF using the ontology's own spellings:

```json
{"@id": "https://example.org/project/Stations", "@type": "StationFetcher",
 "region": "CO", "maxRetries": {"@value": "5", "@type": "xsd:integer"}}
```

- `@type` is the relative class name; dispatch on it.
- A plain string is a string; a typed or language-tagged literal is `{"@value": ..., "@type"|"@language": ...}`;
  a repeated property is an array; a blank node is a nested object; a named node is `{"@id": ...}`.
  Accept both the bare and `@value` forms of a literal.

Write output as newline delimited JSON, one node object per line, using keys the `@context` resolves:

```json
{"@id": "https://geoconnex.us/ref/states/08", "@type": "schema:Place", "schema:name": "Colorado", "gsp:hasGeometry": {"@type": "sf:Point", "gsp:asWKT": {"@value": "POINT(-105.5 39)", "@type": "gsp:wktLiteral"}}}
```

- Each line must be a complete JSON object. Do not print a wrapping array or pretty print across lines.
- A relative `@id` resolves against the project's namespace, not the module's. Use absolute IRIs for
  anything owned by an external source.
- Only stdout is data. Logs and progress go to stderr, which `sal` surfaces as warnings.
- On failure, print an error node and exit non-zero; `sal` reports the message and fails the run:
  `{"@type": "salmodule:Error", "rdfs:comment": "reference server returned 503"}`

### Handing over files

To deliver a file, write it completely inside the container, then reference it as an IRI object:

```json
{"@id": "https://example.org/project/dataset", "schema:hasPart": {"@id": "file:///tmp/stations.csv"}}
```

`sal` copies the file out while the task is still running, so the write must be finished before the
line naming it is printed. The path must be absolute (`file:///`). Each file is copied once; a second
reference is skipped with a warning. In the project the reference becomes `urn:sha256:<digest>` with
`rdfs:label` (file name), `dcterms:modified`, and `owl:versionIRI` attached, and the copy sits in
`.sal/data/blobs`. A directory cannot be handed over; archive it first.

## 3. Dockerfile

Any base image works. The entrypoint must accept `salmodule ontology` and `salmodule run` as
arguments, and the image must run without a network or volume unless the task itself needs one.

```dockerfile
FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY main.py .
ENTRYPOINT ["python", "main.py"]
```

The image is tagged with the git commit it was built from and reused across runs. Commit and push
before expecting a project to see a change; a project already pinned to an older commit needs
`sal build --no-cache` to move.

## 4. Test locally

```sh
docker build -t mymodule .
docker run --rm mymodule salmodule ontology | jq .                          # valid JSON-LD?
docker run --rm -e SALMODULE_TASK_INSTANCE='{"@id":"https://example.org/x","@type":"StationFetcher","region":"CO"}' \
  mymodule salmodule run | head                                             # one JSON object per line?
sal salmodule inspect salmodule://github.com/owner/repo                     # what sal will see once pushed
```

Then reference it from a SAL project (see the `sal` skill) and run `sal run --force` in a scratch
checkout to confirm the triples and files land.

## Checklist

- [ ] `Dockerfile` in the repository root; image builds from a clean clone.
- [ ] `salmodule ontology` prints JSON-LD with a `@context`, an `owl:Ontology` node, relative terms,
      and at least one class that subclasses a `salmodule:` task class.
- [ ] Every configuration property is declared in the ontology.
- [ ] `salmodule run` reads `SALMODULE_TASK_INSTANCE`, writes NDJSON to stdout, logs to stderr.
- [ ] Failures print a `salmodule:Error` node and exit non-zero.
- [ ] Files are fully written before the line that references them with `file:///`.
- [ ] Repository is pushed; `sal salmodule inspect` succeeds against it.
