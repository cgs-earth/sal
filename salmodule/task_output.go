package salmodule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/pkg"
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

// TaskOutputSink receives each non-empty line a task writes to stdout, trimmed
// of surrounding whitespace, as it is read, with its 1-based line number. An
// error it returns stops the task.
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
type TaskOutputParser struct {
	ontology *ModuleOntology
	graph    *rdflibgo.Graph
	base     string

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
}

// NewTaskOutputParser returns the parser that adds what a task of this module
// writes to graph, resolving relative IRIs against base.
func (o *ModuleOntology) NewTaskOutputParser(graph *rdflibgo.Graph, base string) *TaskOutputParser {
	head := []byte(`{"@graph":[`)
	if len(o.Context) > 0 {
		head = append(append([]byte(`{"@context":`), o.Context...), `,"@graph":[`...)
	}
	parser := &TaskOutputParser{ontology: o, graph: graph, base: base, head: head, errorNodes: map[string]rdflibgo.Subject{}}
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
			return fmt.Errorf("SAL module %s wrote invalid JSON on output line %d: %w", p.ontology.Namespace, lineNumber, err)
		}
		return fmt.Errorf("SAL module %s wrote invalid JSON on output line %d", p.ontology.Namespace, lineNumber)
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
// failed on stdout before it exits.
func (p *TaskOutputParser) Finish() error {
	if err := p.flush(); err != nil {
		return err
	}
	if len(p.errorNodes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(p.errorNodes))
	for key := range p.errorNodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var messages []string
	for _, key := range keys {
		pkg.Triples(p.graph, p.errorNodes[key], nil, nil)(func(triple rdflibgo.Triple) bool {
			if literal, ok := triple.Object.(rdflibgo.Literal); ok && isCommentPredicate(triple.Predicate.Value()) {
				messages = append(messages, literal.Lexical())
			}
			return true
		})
	}
	if len(messages) == 0 {
		messages = append(messages, "no message provided")
	}
	return TaskError{Namespace: p.ontology.Namespace, Messages: messages}
}

// FileTriples returns every triple parsed so far that names a file:/// IRI as
// its subject or object, for LinkCopiedFiles to rewrite.
func (p *TaskOutputParser) FileTriples() []rdflibgo.Triple { return p.fileTriples }

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
		return fmt.Errorf("parse output lines %d-%d of SAL module %s: %w", first, last, p.ontology.Namespace, err)
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
		if triple.Predicate.Equal(rdflibgo.RDF.Type) {
			if class, ok := object.(rdflibgo.URIRef); ok && class.Value() == Namespace+"Error" {
				p.errorNodes[subject.String()] = subject
			}
		}
		if isFileIRI(subject) || isFileIRI(object) {
			p.fileTriples = append(p.fileTriples, rdflibgo.Triple{Subject: subject, Predicate: triple.Predicate, Object: object})
		}
		pkg.AddTriple(p.graph, subject, triple.Predicate, object)
		return true
	})
	return nil
}

// isCommentPredicate reports whether an IRI expresses rdfs:comment. Module
// ontologies do not always bind the rdfs prefix in their @context, in which case
// the JSON-LD processor keeps the term verbatim as "rdfs:comment".
func isCommentPredicate(iri string) bool {
	return iri == rdflibgo.RDFS.Comment.Value() || strings.HasSuffix(iri, ":comment")
}
