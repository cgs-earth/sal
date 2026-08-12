package pkg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// SalConfigPath returns the path to .sal/config.jsonld, the single file that
// carries both the project ontology `sal import` maintains and the pinned
// vocabulary versions `sal build` records. The two used to be separate files;
// they are condensed into one JSON-LD document with one @graph so a project
// has a single file describing everything it resolves and materializes
// against.
func SalConfigPath() (string, error) {
	projectDir, err := SALProjectDir(os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(projectDir, ".sal", "config.jsonld"), nil
}

// SalConfigContext is the @context every writer of .sal/config.jsonld emits.
// It is the union of every prefix either the project ontology node or a
// pinned vocabulary node can use, so that whichever of the two last wrote the
// file always leaves every CURIE in it resolvable, including the other
// node kind's, which that writer never inspects.
var SalConfigContext = map[string]string{
	"dc":      "http://purl.org/dc/elements/1.1/",
	"owl":     "http://www.w3.org/2002/07/owl#",
	"dcterms": "http://purl.org/dc/terms/",
	"xsd":     "http://www.w3.org/2001/XMLSchema#",
	"rdfs":    "http://www.w3.org/2000/01/rdf-schema#",
}

// ConfigDocument is the outer JSON-LD envelope of .sal/config.jsonld. Graph
// elements are kept as raw JSON so that a writer which only understands one
// node kind (the project ontology, or a pinned vocabulary) can carry the
// other kind's nodes forward byte for byte without needing to understand them.
type ConfigDocument struct {
	Context map[string]string `json:"@context"`
	Graph   []json.RawMessage `json:"@graph"`
}

// ReadConfigDocument reads .sal/config.jsonld. A missing file is not an
// error; it returns an empty document, since neither the ontology nor any
// pins are required to exist yet.
func ReadConfigDocument(path string) (*ConfigDocument, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &ConfigDocument{Context: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc ConfigDocument
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	if doc.Context == nil {
		doc.Context = map[string]string{}
	}
	return &doc, nil
}

// WriteConfigDocument writes .sal/config.jsonld, creating .sal if needed.
func WriteConfigDocument(path string, doc *ConfigDocument) error {
	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}

// configNodeVersionIRIProbe reads just enough of a graph node to tell a
// pinned vocabulary node apart from the project ontology node: the former
// always carries owl:versionIRI, and the latter never does.
type configNodeVersionIRIProbe struct {
	ID         string          `json:"@id"`
	VersionIRI json.RawMessage `json:"owl:versionIRI"`
}

// PartitionConfigGraph splits a config document's @graph into the project
// ontology node, if there is one, and the pinned vocabulary nodes. This is
// the one place that discriminates between the two node kinds, so that the
// importation package and the validate package, which each own one kind, stay
// in agreement about which nodes are theirs.
func PartitionConfigGraph(graph []json.RawMessage) (ontology json.RawMessage, pinned []json.RawMessage, err error) {
	for _, raw := range graph {
		var probe configNodeVersionIRIProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, nil, err
		}
		if probe.VersionIRI != nil {
			pinned = append(pinned, raw)
			continue
		}
		if ontology != nil {
			return nil, nil, fmt.Errorf("more than one node in @graph without owl:versionIRI; only the project ontology should lack one")
		}
		ontology = raw
	}
	return ontology, pinned, nil
}

// AssembleConfigGraph orders a config document's @graph with the project
// ontology node first, if there is one, followed by the pinned vocabulary
// nodes sorted by @id, so that a rewrite by either writer is a stable diff.
func AssembleConfigGraph(ontology json.RawMessage, pinned []json.RawMessage) ([]json.RawMessage, error) {
	sorted := append([]json.RawMessage{}, pinned...)
	ids := make([]string, len(sorted))
	for i, raw := range sorted {
		var probe configNodeVersionIRIProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, err
		}
		ids[i] = probe.ID
	}
	sort.Slice(sorted, func(i, j int) bool { return ids[i] < ids[j] })

	graph := make([]json.RawMessage, 0, len(sorted)+1)
	if ontology != nil {
		graph = append(graph, ontology)
	}
	return append(graph, sorted...), nil
}

// SplitJSONLDDocument separates a standalone JSON-LD document's @context from
// its node, so the node can be embedded as one element of another document's
// @graph. doc may either be a single flat node or a document wrapping exactly
// one node in @graph, since serializing a graph with one subject can come out
// either way.
func SplitJSONLDDocument(doc []byte) (context map[string]string, node json.RawMessage, err error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, nil, err
	}
	if raw, ok := fields["@context"]; ok {
		if err := json.Unmarshal(raw, &context); err != nil {
			return nil, nil, err
		}
		delete(fields, "@context")
	}
	if raw, ok := fields["@graph"]; ok {
		var nodes []json.RawMessage
		if err := json.Unmarshal(raw, &nodes); err != nil {
			return nil, nil, err
		}
		if len(nodes) != 1 {
			return nil, nil, fmt.Errorf("expected exactly one node in @graph, got %d", len(nodes))
		}
		return context, nodes[0], nil
	}
	node, err = json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	return context, node, nil
}

// JoinJSONLDDocument is the inverse of SplitJSONLDDocument: it wraps a single
// graph node with the @context it should be parsed against, so it can be
// parsed on its own as a standalone JSON-LD document.
func JoinJSONLDDocument(context map[string]string, node json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(node, &fields); err != nil {
		return nil, err
	}
	ctx, err := json.Marshal(context)
	if err != nil {
		return nil, err
	}
	fields["@context"] = ctx
	return json.Marshal(fields)
}
