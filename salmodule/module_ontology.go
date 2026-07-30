package salmodule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
)

// ModuleOntology is the JSON-LD vocabulary a SAL module prints in response to
// its ontology command.
type ModuleOntology struct {
	// Namespace is the vocabulary base the ontology's relative terms resolve against.
	Namespace string
	// Document is the raw JSON-LD the module printed.
	Document []byte
	// Context is the ontology's @context, injected into the JSON a module task
	// writes so that its keys resolve to the module's vocabulary.
	Context json.RawMessage
	// TaskInstanceEnvVar names the environment variable carrying the task
	// instance passed to the run command.
	TaskInstanceEnvVar string

	taskClasses map[string]bool
}

// IsTaskClass reports whether the ontology declares iri as a subclass of one of
// the SAL Module ontology's task classes.
func (o *ModuleOntology) IsTaskClass(iri string) bool {
	return o.taskClasses[iri]
}

// parseModuleOntology reads the vocabulary a module printed, resolving its
// relative terms against the module's salmodule:// namespace.
func parseModuleOntology(namespace string, document []byte) (*ModuleOntology, error) {
	var envelope struct {
		Context json.RawMessage `json:"@context"`
	}
	if err := json.Unmarshal(document, &envelope); err != nil {
		return nil, fmt.Errorf("parse ontology of %s: %w", namespace, err)
	}

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(namespace))
	if err := jsonld.Parse(graph, bytes.NewReader(document), jsonld.WithBase(namespace), jsonld.WithUnboundedLines()); err != nil {
		return nil, fmt.Errorf("parse ontology of %s: %w", namespace, err)
	}

	return &ModuleOntology{
		Namespace:          namespace,
		Document:           document,
		Context:            envelope.Context,
		TaskInstanceEnvVar: taskInstanceEnvVar(graph),
		taskClasses:        taskClasses(graph),
	}, nil
}

// taskInstanceEnvVar returns the environment variable an ontology declares for
// its task instances, falling back to the name in the SAL Module specification.
func taskInstanceEnvVar(graph *rdflibgo.Graph) string {
	name := DefaultTaskInstanceEnvVar
	ontologies := map[string]bool{}
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if triple.Predicate.Equal(rdflibgo.RDF.Type) && triple.Object.Equal(rdflibgo.OWL.Ontology) {
			if subject, ok := triple.Subject.(rdflibgo.URIRef); ok {
				ontologies[subject.Value()] = true
			}
		}
		return true
	})

	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if triple.Predicate.Value() != Namespace+"taskInstanceEnvVar" {
			return true
		}
		subject, ok := triple.Subject.(rdflibgo.URIRef)
		if !ok || !ontologies[subject.Value()] {
			return true
		}
		if literal, ok := triple.Object.(rdflibgo.Literal); ok && literal.Lexical() != "" {
			name = literal.Lexical()
		}
		return true
	})
	return name
}

// taskClasses returns every class the ontology declares, directly or
// transitively, as a subclass of a SAL Module task class.
func taskClasses(graph *rdflibgo.Graph) map[string]bool {
	parents := map[string][]string{}
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if !isSubClassOfPredicate(triple.Predicate.Value()) {
			return true
		}
		subject, subjectOK := triple.Subject.(rdflibgo.URIRef)
		object, objectOK := triple.Object.(rdflibgo.URIRef)
		if subjectOK && objectOK {
			parents[subject.Value()] = append(parents[subject.Value()], object.Value())
		}
		return true
	})

	classes := map[string]bool{}
	for class := range parents {
		if inheritsFromTaskClass(class, parents, map[string]bool{}) {
			classes[class] = true
		}
	}
	return classes
}

func inheritsFromTaskClass(iri string, parents map[string][]string, seen map[string]bool) bool {
	if seen[iri] {
		return false
	}
	seen[iri] = true
	for _, parent := range parents[iri] {
		if IsTaskBaseClass(parent) || inheritsFromTaskClass(parent, parents, seen) {
			return true
		}
	}
	return false
}

// isSubClassOfPredicate reports whether an IRI expresses rdfs:subClassOf. Module
// ontologies do not always bind the rdfs prefix in their @context, in which case
// the JSON-LD processor keeps the term verbatim as "rdfs:subClassOf".
func isSubClassOfPredicate(iri string) bool {
	return iri == rdflibgo.RDFS.SubClassOf.Value() || strings.HasSuffix(iri, ":subClassOf")
}
