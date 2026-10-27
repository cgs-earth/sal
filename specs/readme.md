# Specifications

Human-editable source documents for the vocabularies SAL implements. Nothing in this folder is generated; edit the files directly.

- `salmodule.ttl` is the SAL Module ontology in Turtle. It is the same document published at <https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#>, and the documentation site renders it verbatim on the "SAL Module TTL Spec" page, so a change here shows up there on the next docs build.
- `salmodule-ontology.schema.json` is the JSON Schema (draft 2020-12) for the ontology document a SAL module prints, and so for the `ontology.jsonld` that `sal init --salmodule` scaffolds. The documentation site renders it verbatim on the "SAL Module JSON Schema" page, and the `specs` Go package embeds it so that `sal init --salmodule` can copy it into a module's `.vscode` directory. It describes the structure the SAL Module specification fixes and leaves everything else open; `specs_test.go` checks that the example module and the sal CLI's own ontology conform to it.
