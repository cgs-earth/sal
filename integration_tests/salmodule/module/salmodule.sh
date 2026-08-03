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
        "xsd": "http://www.w3.org/2001/XMLSchema#",
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
            "rdfs:comment": "Emits a fixed set of schema:Place nodes, narrowed by the properties its task instance is configured with.",
            "rdfs:subClassOf": {"@id": "salmodule:Task"}
        },
        {
            "@id": "region",
            "@type": "owl:DatatypeProperty",
            "rdfs:comment": "Selects which places a run emits. Every place is emitted when it is left unset.",
            "rdfs:domain": {"@id": "StaticPlaceProducer"},
            "rdfs:range": {"@id": "xsd:string"}
        },
        {
            "@id": "labelled",
            "@type": "owl:DatatypeProperty",
            "rdfs:comment": "Emits an rdfs:label alongside each place's schema:name when true.",
            "rdfs:domain": {"@id": "StaticPlaceProducer"},
            "rdfs:range": {"@id": "xsd:boolean"}
        },
        {
            "@id": "fail",
            "@type": "owl:DatatypeProperty",
            "rdfs:comment": "Set on a task instance to ask this module to report a failure.",
            "rdfs:domain": {"@id": "StaticPlaceProducer"},
            "rdfs:range": {"@id": "xsd:boolean"}
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

# string_property reads a plain string property of the task instance, which SAL
# writes as a bare JSON string because an untyped RDF literal needs no value object.
string_property() {
    printf '%s' "${SALMODULE_TASK_INSTANCE}" | sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p'
}

# typed_property reads a property whose RDF literal carries a datatype. SAL keeps
# the datatype, so the value arrives as a JSON-LD value object rather than as a
# bare JSON string, number, or boolean.
typed_property() {
    printf '%s' "${SALMODULE_TASK_INSTANCE}" |
        sed -n 's/.*"'"$1"'":{\([^}]*\)}.*/\1/p' |
        sed -n 's/.*"@value":"\([^"]*\)".*/\1/p'
}

# place prints one schema:Place node, with an rdfs:label only when the task
# instance asked for one.
place() {
    if [ "$(typed_property labelled)" = "true" ]; then
        printf '{"@id":"https://example.test/place/%s","@type":"schema:Place","schema:name":"%s","rdfs:label":"%s"}\n' "$1" "$2" "$2"
    else
        printf '{"@id":"https://example.test/place/%s","@type":"schema:Place","schema:name":"%s"}\n' "$1" "$2"
    fi
}

run() {
    if [ -z "${SALMODULE_TASK_INSTANCE:-}" ]; then
        error "SALMODULE_TASK_INSTANCE is not set"
        exit 1
    fi

    # the task instance drives the run, so tests can ask this module to fail
    if [ "$(typed_property fail)" = "true" ]; then
        error "the task instance asked this module to fail"
        exit 1
    fi

    # region narrows what this run emits, which is how the tests check that the
    # properties a project configures its instance with reach the container and
    # change the data it produces
    case "$(string_property region)" in
        west) place tahoe "Lake Tahoe" ;;
        east) place erie "Lake Erie" ;;
        "") place tahoe "Lake Tahoe" ; place erie "Lake Erie" ;;
        *) error "unknown region '$(string_property region)'" ; exit 1 ;;
    esac
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
