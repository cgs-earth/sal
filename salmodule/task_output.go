package salmodule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
)

// TaskError reports the salmodule:Error nodes a module task emitted to say
// why it failed. Messages holds one entry per node, "label: comment" for a
// node carrying both the rdfs:label and the rdfs:comment the SAL Module
// specification requires of an error, and for a node missing either a message
// that says so alongside whatever the node did carry, so a module that
// reports its failure badly is told as much without the report being hidden.
type TaskError struct {
	Namespace string
	Messages  []string
}

func (e TaskError) Error() string {
	return fmt.Sprintf("SAL module %s reported an error: %s", e.Namespace, strings.Join(e.Messages, "; "))
}

// TaskOutputSink receives each node a task writes to one of its output
// streams, the line trimmed of surrounding whitespace, as it is read, with
// its 1-based line number on that stream. An error it returns stops the task.
type TaskOutputSink func(line []byte, lineNumber int) error

// A task's output is parsed in batches of lines rather than line by line, so
// that the module's @context is processed once per batch rather than once per
// node, and rather than whole, so that the memory a task costs is bounded by
// the batch rather than by everything it wrote. A batch is parsed once it holds
// this many lines or this many bytes, whichever comes first.
const (
	taskOutputBatchLines = 1000
	taskOutputBatchBytes = 8 << 20
)

// TaskOutputParser turns the newline delimited JSON a module task writes to
// stdout into RDF as the lines arrive, adding the triples straight to the
// graph it was made for. A task emits plain JSON, so the module ontology's
// @context is injected to resolve the keys against the module's vocabulary.
// Nothing the task wrote is kept once its batch has been parsed, which is what
// lets a task produce far more output than fits in memory as JSON.
//
// A relative IRI in the output resolves against base, the namespace of the
// project the task was run for, not against the module's own namespace. What a
// task produces is instance data belonging to the project that configured and
// ran it; the module's namespace names its vocabulary, and a module that wants
// its output under some other namespace writes absolute IRIs.
//
// Line is the TaskOutputSink to hand RunTask; Finish parses whatever is still
// pending once the task has ended and reports the errors the task declared.
//
// The same parser reads a task's stderr, through NewTaskStderrParser, into a
// graph of its own: the nodes there are the task's control channel, never
// its data, and only the salmodule:Error nodes among them are of interest.
type TaskOutputParser struct {
	ontology *ModuleOntology
	graph    *rdflibgo.Graph
	base     string
	// stream is "stdout" or "stderr", which stream the lines come from, for
	// the messages about them
	stream string

	// head opens the JSON-LD document every batch is parsed as, the module's
	// @context followed by the start of the @graph array the lines go in
	head []byte
	// pending is the document of the batch being assembled, head included
	pending          bytes.Buffer
	pendingLines     int
	firstPendingLine int
	lastLine         int

	// errorNodes are the subjects typed salmodule:Error so far, by their key,
	// whose messages Finish reports
	errorNodes map[string]rdflibgo.Subject
	// fileTriples are the triples naming a file:/// IRI as subject or object,
	// which LinkCopiedFiles rewrites once the copies have landed
	fileTriples []rdflibgo.Triple
	// vocabularies are the namespaces of every predicate, class, and
	// datatype the output has used so far, which the build pins; see
	// Vocabularies
	vocabularies map[string]bool
}

// NewTaskOutputParser returns the parser that adds what a task of this module
// writes to stdout to graph, resolving relative IRIs against base.
func (o *ModuleOntology) NewTaskOutputParser(graph *rdflibgo.Graph, base string) *TaskOutputParser {
	return o.newTaskOutputParser(graph, base, "stdout")
}

// NewTaskStderrParser returns the parser for the nodes a task of this module
// writes to stderr, its control channel: the salmodule:Error nodes a failing
// task reports why it failed with, which Finish reports as a TaskError. The
// nodes are parsed the way stdout is, with the module's @context and against
// base, into a graph of the parser's own, since nothing on stderr is data
// for the graph being built. RunTask hands Line only the lines of stderr that
// are JSON objects; the rest are log lines it shows itself.
func (o *ModuleOntology) NewTaskStderrParser(base string) *TaskOutputParser {
	return o.newTaskOutputParser(rdflibgo.NewGraph(rdflibgo.WithBase(base)), base, "stderr")
}

func (o *ModuleOntology) newTaskOutputParser(graph *rdflibgo.Graph, base string, stream string) *TaskOutputParser {
	head := []byte(`{"@graph":[`)
	if len(o.Context) > 0 {
		head = append(append([]byte(`{"@context":`), o.Context...), `,"@graph":[`...)
	}
	parser := &TaskOutputParser{ontology: o, graph: graph, base: base, stream: stream, head: head, errorNodes: map[string]rdflibgo.Subject{}, vocabularies: map[string]bool{}}
	parser.pending.Write(head)
	return parser
}

// Line adds one line of the task's output to the batch being assembled and
// parses the batch once it is full. A line that is not a JSON object is an
// error, since every line of a task's output is one node.
func (p *TaskOutputParser) Line(line []byte, lineNumber int) error {
	if len(line) == 0 || line[0] != '{' || !json.Valid(line) {
		// decoding into a map is what names the problem with the line
		var node map[string]any
		if err := json.Unmarshal(line, &node); err != nil {
			return fmt.Errorf("SAL module %s wrote invalid JSON on %s line %d: %w", p.ontology.Namespace, p.stream, lineNumber, err)
		}
		return fmt.Errorf("SAL module %s wrote invalid JSON on %s line %d", p.ontology.Namespace, p.stream, lineNumber)
	}
	if p.pendingLines == 0 {
		p.firstPendingLine = lineNumber
	} else {
		p.pending.WriteByte(',')
	}
	p.pending.Write(line)
	p.pendingLines++
	p.lastLine = lineNumber
	if p.pendingLines >= taskOutputBatchLines || p.pending.Len() >= taskOutputBatchBytes {
		return p.flush()
	}
	return nil
}

// Finish parses the lines still pending and reports the salmodule:Error nodes
// the task emitted, if any, as a TaskError. It is called once the task has
// ended, whether or not it succeeded, since a failing task reports why it
// failed before it exits. The SAL Module specification has a task report its
// errors on stderr; one reported on stdout, where a module written before that
// was settled puts it, still fails the task, with a warning that it is out
// of place. An error must carry both an rdfs:label and an rdfs:comment, as
// the stderrShape on salmodule:Task requires; a node missing either is still
// reported, since it is the task's own account of its failure, but the
// message names what is missing.
func (p *TaskOutputParser) Finish() error {
	if err := p.flush(); err != nil {
		return err
	}
	if len(p.errorNodes) == 0 {
		return nil
	}
	if p.stream == "stdout" {
		slog.Warn(p.ontology.Namespace + " reported a salmodule:Error on stdout; the SAL Module specification has a task report its errors on stderr, where sal reads them the same way, and keeps stdout for data")
	}
	keys := make([]string, 0, len(p.errorNodes))
	for key := range p.errorNodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	messages := make([]string, 0, len(keys))
	for _, key := range keys {
		var label, comment string
		pkg.Triples(p.graph, p.errorNodes[key], nil, nil)(func(triple rdflibgo.Triple) bool {
			literal, ok := triple.Object.(rdflibgo.Literal)
			if !ok {
				return true
			}
			switch {
			case isRDFSPredicate(triple.Predicate.Value(), "label"):
				label = literal.Lexical()
			case isRDFSPredicate(triple.Predicate.Value(), "comment"):
				comment = literal.Lexical()
			}
			return true
		})
		messages = append(messages, errorNodeMessage(label, comment))
	}
	return TaskError{Namespace: p.ontology.Namespace, Messages: messages}
}

// errorNodeMessage renders one salmodule:Error node for a TaskError from the
// rdfs:label and rdfs:comment it carried, either of which may be missing.
func errorNodeMessage(label, comment string) string {
	const required = "a salmodule:Error must carry both an rdfs:label and an rdfs:comment"
	switch {
	case label != "" && comment != "":
		return label + ": " + comment
	case label != "":
		return label + " (the error node has no rdfs:comment; " + required + ")"
	case comment != "":
		return comment + " (the error node has no rdfs:label; " + required + ")"
	}
	return "the error node has neither an rdfs:label nor an rdfs:comment; " + required
}

// FileTriples returns every triple parsed so far that names a file:/// IRI as
// its subject or object, for LinkCopiedFiles to rewrite.
func (p *TaskOutputParser) FileTriples() []rdflibgo.Triple { return p.fileTriples }

// Vocabularies returns, sorted, the namespace of every term the output has
// used so far as a predicate, as the class of a typed node, or as a literal's
// datatype: what the output's triples are stated in, which the build pins so
// that the vocabularies the table ends up holding terms of are the ones the
// project lists. Only the output counts: a prefix the module's own @context
// binds without the output using it, sh for the shapes of its ontology say,
// is not among them, and neither is a namespace that only names subjects or
// objects, since those are instances rather than vocabulary.
func (p *TaskOutputParser) Vocabularies() []string {
	namespaces := make([]string, 0, len(p.vocabularies))
	for namespace := range p.vocabularies {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces
}

// VocabularyNamespace is the namespace a term IRI belongs to: everything up
// to and including its last "#", else its last "/". An IRI with neither, such
// as a urn:, belongs to no namespace and reports "".
func VocabularyNamespace(iri string) string {
	if at := strings.LastIndex(iri, "#"); at >= 0 {
		return iri[:at+1]
	}
	if at := strings.LastIndex(iri, "/"); at >= 0 {
		return iri[:at+1]
	}
	return ""
}

// noteVocabulary records the namespace of a term the output used, if it has one.
func (p *TaskOutputParser) noteVocabulary(iri string) {
	if namespace := VocabularyNamespace(iri); namespace != "" {
		p.vocabularies[namespace] = true
	}
}

// flush parses the pending batch as one JSON-LD document and adds its triples
// to the graph. The JSON-LD processor labels the blank nodes of every
// document from _:b0, so each batch's blank nodes are given fresh labels on
// the way in, the same way the Turtle parser labels an unnamed one.
func (p *TaskOutputParser) flush() error {
	if p.pendingLines == 0 {
		return nil
	}
	p.pending.WriteString("]}")
	first, last := p.firstPendingLine, p.lastLine
	batch := rdflibgo.NewGraph(rdflibgo.WithBase(p.base))
	err := jsonld.Parse(context.Background(), batch, bytes.NewReader(p.pending.Bytes()), jsonld.WithBase(p.base), jsonld.WithUnboundedLines())
	p.pending.Reset()
	p.pending.Write(p.head)
	p.pendingLines = 0
	if err != nil {
		return fmt.Errorf("parse %s lines %d-%d of SAL module %s: %w", p.stream, first, last, p.ontology.Namespace, err)
	}

	blanks := map[string]rdflibgo.BNode{}
	relabel := func(term rdflibgo.Term) rdflibgo.Term {
		blank, ok := term.(rdflibgo.BNode)
		if !ok {
			return term
		}
		fresh, seen := blanks[blank.Value()]
		if !seen {
			fresh = rdflibgo.NewBNode()
			blanks[blank.Value()] = fresh
		}
		return fresh
	}
	pkg.Triples(batch, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		subject := relabel(triple.Subject).(rdflibgo.Subject)
		object := relabel(triple.Object)
		p.noteVocabulary(triple.Predicate.Value())
		if triple.Predicate.Equal(rdflibgo.RDF.Type) {
			if class, ok := object.(rdflibgo.URIRef); ok {
				p.noteVocabulary(class.Value())
				if class.Value() == Namespace+"Error" {
					p.errorNodes[subject.String()] = subject
				}
			}
		}
		if literal, ok := object.(rdflibgo.Literal); ok {
			p.noteVocabulary(literal.Datatype().Value())
		}
		if isFileIRI(subject) || isFileIRI(object) {
			p.fileTriples = append(p.fileTriples, rdflibgo.Triple{Subject: subject, Predicate: triple.Predicate, Object: object})
		}
		pkg.AddTriple(p.graph, subject, triple.Predicate, object)
		return true
	})
	return nil
}

// isRDFSPredicate reports whether an IRI expresses the rdfs term named, label
// or comment. Module ontologies do not always bind the rdfs prefix in their
// @context, in which case the JSON-LD processor keeps the term verbatim as
// "rdfs:comment".
func isRDFSPredicate(iri string, term string) bool {
	return iri == "http://www.w3.org/2000/01/rdf-schema#"+term || strings.HasSuffix(iri, ":"+term)
}
