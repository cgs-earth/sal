#!/bin/sh
# A minimal SAL module used to exercise SAL's docker round trip. It implements
# the SAL Module command line interface with hardcoded output so that the
# container builds and runs in about a second.
set -eu

ontology() {
    cat <<'ONTOLOGY'
{
    "@context": {
        "owl": "http://www.w3.org/2002/07/owl#",
        "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
        "schema": "https://schema.org/",
        "salmodule": "https://w3id.org/sal/cgs-earth/sal-module-spec/salmodule#"
    },
    "@graph": [
        {
            "@id": ".",
            "@type": "owl:Ontology",
            "rdfs:label": "SAL Integration Test Module",
            "owl:versionInfo": "0.0.1"
        },
        {
            "@id": "StaticPlaceProducer",
            "@type": "owl:Class",
            "rdfs:label": "Static Place Producer",
            "rdfs:comment": "Emits a fixed set of schema:Place nodes.",
            "rdfs:subClassOf": {"@id": "salmodule:Task"}
        },
        {
            "@id": "NotATask",
            "@type": "owl:Class",
            "rdfs:comment": "Declared so tests can check that a non-task class is never run."
        }
    ]
}
ONTOLOGY
}

# error prints a salmodule:Error node, which is how a task reports a failure.
error() {
    printf '{"@type":"salmodule:Error","rdfs:label":"TaskFailed","rdfs:comment":"%s"}\n' "$1"
}

run() {
    if [ -z "${SALMODULE_TASK_INSTANCE:-}" ]; then
        error "SALMODULE_TASK_INSTANCE is not set"
        exit 1
    fi

    # the task instance drives the run, so tests can ask this module to fail
    if printf '%s' "${SALMODULE_TASK_INSTANCE}" | grep -q '"fail":true'; then
        error "the task instance asked this module to fail"
        exit 1
    fi

    cat <<'NODES'
{"@id":"https://example.test/place/tahoe","@type":"schema:Place","schema:name":"Lake Tahoe"}
{"@id":"https://example.test/place/erie","@type":"schema:Place","schema:name":"Lake Erie"}
NODES
}

usage() {
    echo "usage: salmodule (ontology|vocab|vocabulary|run)" >&2
    exit 1
}

[ "${1:-}" = "salmodule" ] || usage
case "${2:-}" in
    ontology | vocab | vocabulary) ontology ;;
    run) run ;;
    *) usage ;;
esac
