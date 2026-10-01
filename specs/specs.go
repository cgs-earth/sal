// Package specs embeds the specification documents under specs/ that the CLI
// hands out verbatim, so that each one is edited in a single place.
package specs

import _ "embed"

// SalModuleOntologySchemaFile is the name sal init --salmodule writes the JSON
// Schema under inside a module's .vscode directory.
const SalModuleOntologySchemaFile = "salmodule-ontology.schema.json"

// SalModuleOntologySchema is the JSON Schema a SAL module's ontology.jsonld is
// checked against while it is edited. It describes the structure the SAL
// Module specification requires of the document: a @context binding the
// prefixes, an owl:Ontology node named ".", at least one task class, and the
// SHACL shapes a task class may carry. sal itself never validates against it;
// the module ontology is parsed as RDF by salmodule.parseModuleOntology.
//
//go:embed salmodule-ontology.schema.json
var SalModuleOntologySchema []byte
