package salmodule

import (
	"encoding/json"
	"fmt"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
)

// TaskInstance renders the JSON-LD node object SAL passes to a module through
// its task instance environment variable. A module is configured in RDF rather
// than with an embedded JSON literal, so the node object is built from the
// instance's own properties: every predicate the module's vocabulary defines is
// an input to the task, and predicates from any other vocabulary are left out
// since only the module knows what configures it.
func (o *ModuleOntology) TaskInstance(graph *rdflibgo.Graph, subject rdflibgo.Subject, classIRI string) (string, error) {
	node := o.nodeObject(graph, subject, map[string]bool{})
	node["@id"] = subject.String()
	// an instance may be typed with several module classes, so the class this
	// task was found under wins over whichever type nodeObject happened to see
	node["@type"] = o.compact(classIRI)

	encoded, err := json.Marshal(node)
	if err != nil {
		return "", fmt.Errorf("encode task instance for %s: %w", classIRI, err)
	}
	return string(encoded), nil
}

// nodeObject collects the properties of subject that the module's own vocabulary
// defines. A predicate used more than once becomes an array, matching the JSON-LD
// convention for multi-valued properties.
func (o *ModuleOntology) nodeObject(graph *rdflibgo.Graph, subject rdflibgo.Subject, seen map[string]bool) map[string]any {
	node := map[string]any{}
	if seen[subject.String()] {
		return node
	}
	seen[subject.String()] = true

	values := map[string][]any{}
	graph.Triples(subject, nil, nil)(func(triple rdflibgo.Triple) bool {
		if triple.Predicate.Equal(rdflibgo.RDF.Type) {
			if object, ok := triple.Object.(rdflibgo.URIRef); ok && strings.HasPrefix(object.Value(), o.Namespace) {
				node["@type"] = o.compact(object.Value())
			}
			return true
		}
		predicate := triple.Predicate.Value()
		if !strings.HasPrefix(predicate, o.Namespace) {
			return true
		}
		term := strings.TrimPrefix(predicate, o.Namespace)
		values[term] = append(values[term], o.instanceValue(graph, triple.Object, seen))
		return true
	})

	for term, list := range values {
		if len(list) == 1 {
			node[term] = list[0]
			continue
		}
		node[term] = list
	}
	return node
}

// instanceValue turns the object of a task instance property into its JSON-LD
// representation.
func (o *ModuleOntology) instanceValue(graph *rdflibgo.Graph, object rdflibgo.Term, seen map[string]bool) any {
	switch object := object.(type) {
	case rdflibgo.Literal:
		switch {
		case object.Language() != "":
			return map[string]any{"@value": object.Lexical(), "@language": object.Language()}
		case object.Datatype().Value() == "" || object.Datatype().Equal(rdflibgo.XSDString):
			return object.Lexical()
		default:
			return map[string]any{"@value": object.Lexical(), "@type": o.compact(object.Datatype().Value())}
		}
	case rdflibgo.BNode:
		// an anonymous node carries structured configuration, and has no
		// identifier the module could dereference, so it is inlined
		return o.nodeObject(graph, object, seen)
	case rdflibgo.URIRef:
		return map[string]any{"@id": o.compact(object.Value())}
	}
	return nil
}

// compact shortens an IRI the way the module's own ontology writes it: a term of
// the module's vocabulary becomes the relative name the module declared it under,
// and any other IRI is prefixed with whichever prefix the ontology's @context
// binds it to. An IRI the @context cannot express is left in full.
func (o *ModuleOntology) compact(iri string) string {
	if local, ok := strings.CutPrefix(iri, o.Namespace); ok {
		return local
	}

	var prefix, namespace string
	for candidate, base := range o.prefixes {
		if strings.HasPrefix(iri, base) && len(base) > len(namespace) {
			prefix, namespace = candidate, base
		}
	}
	if namespace == "" {
		return iri
	}
	return prefix + ":" + strings.TrimPrefix(iri, namespace)
}

// contextPrefixes reads the prefix bindings of an ontology's @context. Only the
// simple `"prefix": "IRI"` form is used; a term definition object binds a term
// rather than a namespace, and so cannot shorten an arbitrary IRI.
func contextPrefixes(context json.RawMessage) map[string]string {
	var bindings map[string]any
	if len(context) == 0 || json.Unmarshal(context, &bindings) != nil {
		return nil
	}

	prefixes := map[string]string{}
	for prefix, binding := range bindings {
		iri, ok := binding.(string)
		if !ok || strings.HasPrefix(prefix, "@") {
			continue
		}
		prefixes[prefix] = iri
	}
	return prefixes
}
