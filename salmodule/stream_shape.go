package salmodule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
	"github.com/tggo/goRDFlib/shacl"
)

// unsupportedShapeConstraints are the SHACL constraint parameters a
// salmodule:stdoutShape or salmodule:stderrShape may not use. A shape using
// one is refused when the validator is built rather than silently ignored, so
// a module author finds out on the first run rather than never.
var unsupportedShapeConstraints = []string{"languageIn", "uniqueLang", "disjoint", "qualifiedValueShapesDisjoint", "sparql"}

// shapeAnnotationProperties are the annotation properties a task class carries
// its shapes with. They are never followed while a shape is being collected,
// so that a shape naming a task class as its target does not drag that class's
// other shapes into the shapes graph.
var shapeAnnotationProperties = []string{Namespace + "stdoutShape", Namespace + "stderrShape", Namespace + "stdinShape", Namespace + "taskShape"}

// StreamShapeError reports the first node of a task's stdout or stderr that
// did not conform to the salmodule:stdoutShape or salmodule:stderrShape its
// class declares for the stream.
type StreamShapeError struct {
	Namespace string
	// Class is the task class whose shape was violated.
	Class string
	// Stream is "stdout" or "stderr", the stream the node was written on and
	// the shape it violated: salmodule:<Stream>Shape.
	Stream string
	// Line is the 1-based line of the stream the node was written on.
	Line       int
	Violations []string
}

func (e StreamShapeError) Error() string {
	return fmt.Sprintf("SAL module %s wrote a node on %s line %d that does not conform to the salmodule:%sShape of %s: %s", e.Namespace, e.Stream, e.Line, e.Stream, e.Class, strings.Join(e.Violations, "; "))
}

// StreamValidator validates the newline delimited JSON a task writes to one
// of its output streams, one node at a time and as each arrives, against the
// SHACL shapes its class declares for that stream with salmodule:stdoutShape
// or salmodule:stderrShape.
type StreamValidator struct {
	namespace string
	class     string
	stream    string
	base      string
	context   json.RawMessage
	shapes    *shacl.Graph
}

// StdoutValidator returns the validator for the stdout of a task of class
// classIRI, built from every salmodule:stdoutShape the class carries, or a
// superclass of it the ontology declares carries. It returns nil when there is
// none, in which case the output is not validated at all and costs nothing.
// Only the shapes themselves, with everything reachable from them, make up the
// shapes graph, so the ontology's other shape annotations never apply to
// stdout. A shape using a constraint SAL does not support is an error.
//
// base is what a relative IRI in the output resolves against, the project
// namespace, the same base TaskOutputParser is given, so a shape sees the
// node exactly as it will be committed.
func (o *ModuleOntology) StdoutValidator(classIRI string, base string) (*StreamValidator, error) {
	return o.streamValidator(classIRI, "stdout", base)
}

// StderrValidator is StdoutValidator for stderr, built from the class's
// salmodule:stderrShape. The SAL Module ontology's own shape for stderr, that
// a salmodule:Error carries an rdfs:label and an rdfs:comment, is not among
// them: TaskOutputParser.Finish reads both itself and reports a node missing
// either as such, rather than stopping the task for the shape of its failure
// report and hiding what it did say.
func (o *ModuleOntology) StderrValidator(classIRI string, base string) (*StreamValidator, error) {
	return o.streamValidator(classIRI, "stderr", base)
}

func (o *ModuleOntology) streamValidator(classIRI string, stream string, base string) (*StreamValidator, error) {
	annotation := Namespace + stream + "Shape"
	shapeNodes := o.streamShapes(classIRI, annotation)
	if len(shapeNodes) == 0 {
		return nil, nil
	}

	shapes := shacl.NewGraph()
	seen := map[string]bool{}
	for _, node := range shapeNodes {
		if err := o.collectShape(node, shapes, seen); err != nil {
			return nil, err
		}
	}
	for _, parameter := range unsupportedShapeConstraints {
		predicate := shacl.IRI(shacl.SH + parameter)
		if shapes.Has(nil, &predicate, nil) {
			return nil, fmt.Errorf("the salmodule:%sShape of %s uses sh:%s, which SAL does not support", stream, classIRI, parameter)
		}
	}
	return &StreamValidator{
		namespace: o.Namespace,
		class:     classIRI,
		stream:    stream,
		base:      base,
		context:   o.Context,
		shapes:    shapes,
	}, nil
}

// streamShapes returns the objects of every statement with the shape
// annotation property on classIRI or on a superclass the ontology declares
// for it.
func (o *ModuleOntology) streamShapes(classIRI string, annotation string) []rdflibgo.Term {
	var shapes []rdflibgo.Term
	predicate := rdflibgo.NewURIRefUnsafe(annotation)
	seen := map[string]bool{}
	queue := []string{classIRI}
	for len(queue) > 0 {
		class := queue[0]
		queue = queue[1:]
		if seen[class] {
			continue
		}
		seen[class] = true
		pkg.Triples(o.graph, rdflibgo.NewURIRefUnsafe(class), &predicate, nil)(func(triple rdflibgo.Triple) bool {
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
func (o *ModuleOntology) collectShape(node rdflibgo.Term, shapes *shacl.Graph, seen map[string]bool) error {
	subject, ok := node.(rdflibgo.Subject)
	if !ok || seen[subject.String()] {
		return nil
	}
	seen[subject.String()] = true

	var triples []rdflibgo.Triple
	pkg.Triples(o.graph, subject, nil, nil)(func(triple rdflibgo.Triple) bool {
		triples = append(triples, triple)
		return true
	})
	for _, triple := range triples {
		if err := shapes.Add(context.Background(), shaclTerm(triple.Subject), shaclTerm(triple.Predicate), shaclTerm(triple.Object)); err != nil {
			return err
		}
		if !slices.Contains(shapeAnnotationProperties, triple.Predicate.Value()) {
			if err := o.collectShape(triple.Object, shapes, seen); err != nil {
				return err
			}
		}
	}
	return nil
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
// lineNumber of the stream, against the shapes. The module's @context is
// injected and the node parsed the way TaskOutputParser parses the whole
// output, so what is validated is the RDF the node will become. A violation is
// returned as a StreamShapeError; a result of any lesser severity is logged
// and does not fail the line.
func (v *StreamValidator) ValidateLine(line []byte, lineNumber int) error {
	var node map[string]any
	if err := json.Unmarshal(line, &node); err != nil {
		return fmt.Errorf("SAL module %s wrote invalid JSON on %s line %d: %w", v.namespace, v.stream, lineNumber, err)
	}
	document := map[string]any{"@graph": []json.RawMessage{json.RawMessage(line)}}
	if len(v.context) > 0 {
		document["@context"] = v.context
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode %s line %d of SAL module %s: %w", v.stream, lineNumber, v.namespace, err)
	}
	data, err := shacl.LoadJsonLD(bytes.NewReader(encoded), v.base, jsonld.WithUnboundedLines())
	if err != nil {
		return fmt.Errorf("parse %s line %d of SAL module %s: %w", v.stream, lineNumber, v.namespace, err)
	}

	var violations []string
	for _, result := range shacl.Validate(data, v.shapes).Results {
		description := describeResult(result)
		switch result.ResultSeverity.Value() {
		case shacl.SH + "Warning":
			slog.Warn(fmt.Sprintf("%s line %d of %s: %s", v.stream, lineNumber, v.class, description))
		case shacl.SH + "Info", shacl.SH + "Debug", shacl.SH + "Trace":
			slog.Info(fmt.Sprintf("%s line %d of %s: %s", v.stream, lineNumber, v.class, description))
		default:
			violations = append(violations, description)
		}
	}
	if len(violations) > 0 {
		return StreamShapeError{Namespace: v.namespace, Class: v.class, Stream: v.stream, Line: lineNumber, Violations: violations}
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
