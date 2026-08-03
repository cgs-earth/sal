package salmodule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
)

// TaskError reports the salmodule:Error nodes a module task emitted instead of data.
type TaskError struct {
	Namespace string
	Messages  []string
}

func (e TaskError) Error() string {
	return fmt.Sprintf("SAL module %s reported an error: %s", e.Namespace, strings.Join(e.Messages, "; "))
}

// GraphFromTaskOutput turns the newline delimited JSON a module task wrote to
// stdout into RDF. A task emits plain JSON, so the module ontology's @context is
// injected to resolve the keys against the module's vocabulary.
func (o *ModuleOntology) GraphFromTaskOutput(output []byte) (*rdflibgo.Graph, error) {
	var nodes []json.RawMessage
	for lineNumber, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var node map[string]any
		if err := json.Unmarshal([]byte(line), &node); err != nil {
			return nil, fmt.Errorf("SAL module %s wrote invalid JSON on output line %d: %w", o.Namespace, lineNumber+1, err)
		}
		nodes = append(nodes, json.RawMessage(line))
	}
	if len(nodes) == 0 {
		return rdflibgo.NewGraph(rdflibgo.WithBase(o.Namespace)), nil
	}

	document := map[string]any{"@graph": nodes}
	if len(o.Context) > 0 {
		document["@context"] = o.Context
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode output of SAL module %s: %w", o.Namespace, err)
	}

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(o.Namespace))
	if err := jsonld.Parse(graph, bytes.NewReader(encoded), jsonld.WithBase(o.Namespace), jsonld.WithUnboundedLines()); err != nil {
		return nil, fmt.Errorf("parse output of SAL module %s: %w", o.Namespace, err)
	}
	if messages := errorMessages(graph); len(messages) > 0 {
		return nil, TaskError{Namespace: o.Namespace, Messages: messages}
	}
	return graph, nil
}

// errorMessages collects the comments attached to any salmodule:Error node a
// task emitted in place of its output.
func errorMessages(graph *rdflibgo.Graph) []string {
	errorNodes := map[string]bool{}
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if !triple.Predicate.Equal(rdflibgo.RDF.Type) {
			return true
		}
		if object, ok := triple.Object.(rdflibgo.URIRef); ok && object.Value() == Namespace+"Error" {
			errorNodes[triple.Subject.String()] = true
		}
		return true
	})
	if len(errorNodes) == 0 {
		return nil
	}

	var messages []string
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if !errorNodes[triple.Subject.String()] || !isCommentPredicate(triple.Predicate.Value()) {
			return true
		}
		if literal, ok := triple.Object.(rdflibgo.Literal); ok {
			messages = append(messages, literal.Lexical())
		}
		return true
	})
	if len(messages) == 0 {
		messages = append(messages, "no message provided")
	}
	return messages
}

// isCommentPredicate reports whether an IRI expresses rdfs:comment. Module
// ontologies do not always bind the rdfs prefix in their @context, in which case
// the JSON-LD processor keeps the term verbatim as "rdfs:comment".
func isCommentPredicate(iri string) bool {
	return iri == rdflibgo.RDFS.Comment.Value() || strings.HasSuffix(iri, ":comment")
}
