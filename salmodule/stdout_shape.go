package salmodule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
	"github.com/tggo/goRDFlib/shacl"
)

// unsupportedShapeConstraints are the SHACL constraint parameters a
// salmodule:stdoutShape may not use. A shape using one is refused when the
// validator is built rather than silently ignored, so a module author finds
// out on the first run rather than never.
var unsupportedShapeConstraints = []string{"languageIn", "uniqueLang", "disjoint", "qualifiedValueShapesDisjoint", "sparql"}

// shapeAnnotationProperties are the annotation properties a task class carries
// its shapes with. They are never followed while a shape is being collected,
// so that a shape naming a task class as its target does not drag that class's
// other shapes into the shapes graph.
var shapeAnnotationProperties = []string{Namespace + "stdoutShape", Namespace + "stdinShape", Namespace + "taskShape"}

// StdoutShapeError reports the first line of a task's output that did not
// conform to the salmodule:stdoutShape its class declares.
type StdoutShapeError struct {
	Namespace string
	// Class is the task class whose stdoutShape was violated.
	Class string
	// Line is the 1-based line of the task's stdout the node was written on.
	Line       int
	Violations []string
}

func (e StdoutShapeError) Error() string {
	return fmt.Sprintf("SAL module %s wrote a node on output line %d that does not conform to the salmodule:stdoutShape of %s: %s", e.Namespace, e.Line, e.Class, strings.Join(e.Violations, "; "))
}

// StdoutValidator validates the newline delimited JSON a task writes to stdout,
// one node at a time and as each arrives, against the SHACL shapes its class
// declares with salmodule:stdoutShape.
type StdoutValidator struct {
	namespace string
	class     string
	base      string
	context   json.RawMessage
	shapes    *shacl.Graph
}

// StdoutValidator returns the validator for the output of a task of class
// classIRI, built from every salmodule:stdoutShape the class carries, or a
// superclass of it the ontology declares carries. It returns nil when there is
// none, in which case the output is not validated at all and costs nothing.
// Only the shapes themselves, with everything reachable from them, make up the
// shapes graph, so the ontology's taskShape and stdinShape annotations never
// apply to stdout. A shape using a constraint SAL does not support is an error.
//
// base is what a relative IRI in the output resolves against, the project
// namespace, the same base GraphFromTaskOutput is given, so a shape sees the
// node exactly as it will be committed.
func (o *ModuleOntology) StdoutValidator(classIRI string, base string) (*StdoutValidator, error) {
	shapeNodes := o.stdoutShapes(classIRI)
	if len(shapeNodes) == 0 {
		return nil, nil
	}

	shapes := shacl.NewGraph()
	seen := map[string]bool{}
	for _, node := range shapeNodes {
		o.collectShape(node, shapes, seen)
	}
	for _, parameter := range unsupportedShapeConstraints {
		predicate := shacl.IRI(shacl.SH + parameter)
		if shapes.Has(nil, &predicate, nil) {
			return nil, fmt.Errorf("the salmodule:stdoutShape of %s uses sh:%s, which SAL does not support", classIRI, parameter)
		}
	}
	return &StdoutValidator{
		namespace: o.Namespace,
		class:     classIRI,
		base:      base,
		context:   o.Context,
		shapes:    shapes,
	}, nil
}

// stdoutShapes returns the objects of every salmodule:stdoutShape statement on
// classIRI or on a superclass the ontology declares for it.
func (o *ModuleOntology) stdoutShapes(classIRI string) []rdflibgo.Term {
	var shapes []rdflibgo.Term
	predicate := rdflibgo.NewURIRefUnsafe(Namespace + "stdoutShape")
	seen := map[string]bool{}
	queue := []string{classIRI}
	for len(queue) > 0 {
		class := queue[0]
		queue = queue[1:]
		if seen[class] {
			continue
		}
		seen[class] = true
		o.graph.Triples(rdflibgo.NewURIRefUnsafe(class), &predicate, nil)(func(triple rdflibgo.Triple) bool {
			shapes = append(shapes, triple.Object)
			return true
		})
		queue = append(queue, o.parents[class]...)
	}
	return shapes
}

// collectShape copies the shape at node into shapes together with everything
// reachable from it: its property shapes, the lists behind sh:in or sh:or, and
// any shape it refers to by IRI.
func (o *ModuleOntology) collectShape(node rdflibgo.Term, shapes *shacl.Graph, seen map[string]bool) {
	subject, ok := node.(rdflibgo.Subject)
	if !ok || seen[subject.String()] {
		return
	}
	seen[subject.String()] = true

	var triples []rdflibgo.Triple
	o.graph.Triples(subject, nil, nil)(func(triple rdflibgo.Triple) bool {
		triples = append(triples, triple)
		return true
	})
	for _, triple := range triples {
		shapes.Add(shaclTerm(triple.Subject), shaclTerm(triple.Predicate), shaclTerm(triple.Object))
		if !slices.Contains(shapeAnnotationProperties, triple.Predicate.Value()) {
			o.collectShape(triple.Object, shapes, seen)
		}
	}
}

// shaclTerm converts a goRDFlib term to the validator's own term type, filling
// in the datatype a plain or language tagged literal carries implicitly.
func shaclTerm(t rdflibgo.Term) shacl.Term {
	switch v := t.(type) {
	case rdflibgo.URIRef:
		return shacl.IRI(v.Value())
	case rdflibgo.BNode:
		return shacl.BlankNode(v.Value())
	case rdflibgo.Literal:
		datatype := v.Datatype().Value()
		switch {
		case v.Language() != "" && datatype == "":
			datatype = shacl.RDF + "langString"
		case datatype == "":
			datatype = shacl.XSD + "string"
		}
		return shacl.Literal(v.Lexical(), datatype, v.Language())
	}
	return shacl.Term{}
}

// ValidateLine checks one node of a task's output, the JSON written on line
// lineNumber of its stdout, against the shapes. The module's @context is
// injected and the node parsed the way GraphFromTaskOutput parses the whole
// output, so what is validated is the RDF the node will become. A violation is
// returned as a StdoutShapeError; a result of any lesser severity is logged
// and does not fail the line.
func (v *StdoutValidator) ValidateLine(line []byte, lineNumber int) error {
	var node map[string]any
	if err := json.Unmarshal(line, &node); err != nil {
		return fmt.Errorf("SAL module %s wrote invalid JSON on output line %d: %w", v.namespace, lineNumber, err)
	}
	document := map[string]any{"@graph": []json.RawMessage{json.RawMessage(line)}}
	if len(v.context) > 0 {
		document["@context"] = v.context
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode output line %d of SAL module %s: %w", lineNumber, v.namespace, err)
	}
	data, err := shacl.LoadJsonLD(bytes.NewReader(encoded), v.base, jsonld.WithUnboundedLines())
	if err != nil {
		return fmt.Errorf("parse output line %d of SAL module %s: %w", lineNumber, v.namespace, err)
	}

	var violations []string
	for _, result := range shacl.Validate(data, v.shapes).Results {
		description := describeResult(result)
		switch result.ResultSeverity.Value() {
		case shacl.SH + "Warning":
			slog.Warn(fmt.Sprintf("Output line %d of %s: %s", lineNumber, v.class, description))
		case shacl.SH + "Info", shacl.SH + "Debug", shacl.SH + "Trace":
			slog.Info(fmt.Sprintf("Output line %d of %s: %s", lineNumber, v.class, description))
		default:
			violations = append(violations, description)
		}
	}
	if len(violations) > 0 {
		return StdoutShapeError{Namespace: v.namespace, Class: v.class, Line: lineNumber, Violations: violations}
	}
	return nil
}

// describeResult renders one validation result as the node it is about and the
// shape's own sh:message, falling back to naming the constraint and the path
// when the shape gives no message.
func describeResult(result shacl.ValidationResult) string {
	var message string
	if len(result.ResultMessages) > 0 {
		parts := make([]string, 0, len(result.ResultMessages))
		for _, m := range result.ResultMessages {
			parts = append(parts, m.Value())
		}
		message = strings.Join(parts, " ")
	} else {
		message = "violates sh:" + strings.TrimSuffix(strings.TrimPrefix(result.SourceConstraintComponent.Value(), shacl.SH), "ConstraintComponent")
		if !result.ResultPath.IsNone() {
			message = "value of " + describeTerm(result.ResultPath) + " " + message
		}
	}
	return describeTerm(result.FocusNode) + " " + message
}

// describeTerm renders a term the way Turtle would.
func describeTerm(t shacl.Term) string {
	switch {
	case t.IsIRI():
		return "<" + t.Value() + ">"
	case t.IsBlank():
		return "_:" + t.Value()
	case t.IsLiteral():
		return fmt.Sprintf("%q", t.Value())
	}
	return "an unnamed node"
}
