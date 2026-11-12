package salmodule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
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

// TripleSink receives the triples a task's output parses into, one at a time,
// already relabelled so that its blank nodes are stable. An error it returns
// stops the task. A build hands the parser one that writes the triples
// straight to Parquet (load.Spool), so that what a task produces is never held
// in memory as a graph.
type TripleSink func(rdflibgo.Triple) error

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

// taskOutputParseWorkers is how many batches of one stream are parsed at
// once. Parsing JSON-LD is what bounds how fast sal reads a task's output, far
// more than the task writing it or the spool writing it to disk, and batches
// parse independently, so a parser keeps this many in flight. Tests set it.
var taskOutputParseWorkers = min(runtime.GOMAXPROCS(0), 8)

// taskOutputBatch is one batch of a stream's lines handed to a parse worker:
// the JSON-LD document the lines make, and where in it each line carrying a
// @context of its own lies.
type taskOutputBatch struct {
	document    []byte
	contexts    [][2]int
	first, last int
}

// parsedBatch is what a parse worker made of a batch: its triples with their
// blank nodes already stable, and the namespaces the @context of its lines
// bound.
type parsedBatch struct {
	triples  []rdflibgo.Triple
	bindings map[string]bool
	err      error
}

// TaskOutputParser turns the newline delimited JSON a module task writes to
// stdout into RDF as the lines arrive, handing the triples straight to the
// TripleSink it was made for, or adding them to a graph. A task emits plain
// JSON, so the module ontology's @context is injected to resolve the keys
// against the module's vocabulary. Nothing the task wrote is kept once its
// batch has been parsed, which is what lets a task produce far more output
// than fits in memory.
//
// The blank nodes of a batch are labelled one connected component at a time
// (pkg.StableBlankNodeComponents), so the labels a node gets depend on that
// node alone and not on the batch it landed in or on anything else the build
// holds. A blank node is therefore local to the line that writes it, which is
// what the SAL Module specification's one node per line means: a label a
// module reuses across lines does not join them.
//
// A triple naming a file:/// IRI is not handed on: it is kept, see
// FileTriples, until the copy it names has landed and LinkCopiedFiles can
// hand it on under the copy's urn:sha256: IRI.
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
// Batches are parsed by up to taskOutputParseWorkers goroutines at once, but
// everything else happens on the goroutine calling Line and Finish, in the
// order the lines were written: the sink sees one batch's triples after the
// previous batch's, exactly as it would were the batches parsed one by one,
// and is never called concurrently. Since a batch is handed on once parsed
// rather than when it fills, a batch that fails to parse, or whose triples the
// sink refuses, fails a later call to Line, or Finish.
//
// The same parser reads a task's stderr, through NewTaskStderrParser, into a
// graph of its own: the nodes there are the task's control channel, never
// its data, and only the salmodule:Error nodes among them are of interest.
type TaskOutputParser struct {
	ontology *ModuleOntology
	// sink is where the triples go
	sink TripleSink
	// errors is the graph the salmodule:Error nodes are read back from, the
	// graph the triples go to when there is one, else a scratch graph that
	// holds only the error nodes' own triples
	errors      *rdflibgo.Graph
	errorsApart bool
	base        string
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
	// pendingContexts is where in pending each line carrying a @context of
	// its own lies
	pendingContexts [][2]int

	// inflight are the batches handed to the parse workers, in the order the
	// lines were written, each delivering its result once parsed; workers
	// holds a slot per batch being parsed
	inflight []chan parsedBatch
	workers  chan struct{}
	// failed is the first error a batch reported, after which nothing more
	// is parsed or handed on
	failed error

	// errorNodes are the subjects typed salmodule:Error so far, by their key,
	// whose messages Finish reports
	errorNodes map[string]rdflibgo.Subject
	// fileTriples are the triples naming a file:/// IRI as subject or object,
	// which LinkCopiedFiles rewrites once the copies have landed
	fileTriples []rdflibgo.Triple
	// terms are the IRIs of every predicate, class, and datatype the output
	// has used so far, and bindings the IRIs the module's @context and the
	// @context of any node bind a prefix or a term to; together they decide
	// what the build pins, see Vocabularies
	terms    map[string]bool
	bindings map[string]bool
}

// NewTaskOutputParser returns the parser that adds what a task of this module
// writes to stdout to graph, resolving relative IRIs against base.
func (o *ModuleOntology) NewTaskOutputParser(graph *rdflibgo.Graph, base string) *TaskOutputParser {
	return o.newTaskOutputParser(graph, nil, base, "stdout")
}

// NewTaskOutputStream returns the parser that hands what a task of this
// module writes to stdout to sink, resolving relative IRIs against base. No
// graph of the output is built; only the triples of the salmodule:Error nodes
// it reports are kept, for Finish.
func (o *ModuleOntology) NewTaskOutputStream(sink TripleSink, base string) *TaskOutputParser {
	return o.newTaskOutputParser(rdflibgo.NewGraph(rdflibgo.WithBase(base)), sink, base, "stdout")
}

// NewTaskStderrParser returns the parser for the nodes a task of this module
// writes to stderr, its control channel: the salmodule:Error nodes a failing
// task reports why it failed with, which Finish reports as a TaskError. The
// nodes are parsed the way stdout is, with the module's @context and against
// base, into a graph of the parser's own, since nothing on stderr is data
// for the graph being built. RunTask hands Line only the lines of stderr that
// are JSON objects; the rest are log lines it shows itself.
func (o *ModuleOntology) NewTaskStderrParser(base string) *TaskOutputParser {
	return o.newTaskOutputParser(rdflibgo.NewGraph(rdflibgo.WithBase(base)), nil, base, "stderr")
}

// newTaskOutputParser makes the parser for one stream. Without a sink the
// triples are added to graph; with one, graph only collects the error nodes.
func (o *ModuleOntology) newTaskOutputParser(graph *rdflibgo.Graph, sink TripleSink, base string, stream string) *TaskOutputParser {
	head := []byte(`{"@graph":[`)
	if len(o.Context) > 0 {
		head = append(append([]byte(`{"@context":`), o.Context...), `,"@graph":[`...)
	}
	parser := &TaskOutputParser{ontology: o, sink: sink, errors: graph, errorsApart: sink != nil, base: base, stream: stream, head: head, errorNodes: map[string]rdflibgo.Subject{}, terms: map[string]bool{}, bindings: map[string]bool{}, workers: make(chan struct{}, max(taskOutputParseWorkers, 1))}
	if sink == nil {
		parser.sink = func(triple rdflibgo.Triple) error {
			pkg.AddTriple(graph, triple.Subject, triple.Predicate, triple.Object)
			return nil
		}
	}
	parser.pending.Write(head)
	if len(o.Context) > 0 {
		var moduleContext any
		if err := json.Unmarshal(o.Context, &moduleContext); err == nil {
			noteBindings(parser.bindings, moduleContext)
		}
	}
	return parser
}

// Line adds one line of the task's output to the batch being assembled and
// parses the batch once it is full. A line that is not a JSON object is an
// error, since every line of a task's output is one node.
func (p *TaskOutputParser) Line(line []byte, lineNumber int) error {
	if p.failed != nil {
		return p.failed
	}
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
	start := p.pending.Len()
	p.pending.Write(line)
	if bytes.Contains(line, []byte("@context")) {
		// a node that carries a context of its own declares vocabularies
		// the module's does not; the worker parsing the batch decodes it
		p.pendingContexts = append(p.pendingContexts, [2]int{start, p.pending.Len()})
	}
	p.pendingLines++
	p.lastLine = lineNumber
	if p.pendingLines >= taskOutputBatchLines || p.pending.Len() >= taskOutputBatchBytes {
		return p.dispatch()
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
	if p.failed == nil {
		_ = p.dispatch()
	}
	// every batch still being parsed is waited for, so no worker outlives
	// the parser, and handed on unless an earlier one failed
	for _, result := range p.inflight {
		parsed := <-result
		if p.failed == nil {
			p.failed = p.deliver(parsed)
		}
	}
	p.inflight = nil
	if p.failed != nil {
		return p.failed
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
		pkg.Triples(p.errors, p.errorNodes[key], nil, nil)(func(triple rdflibgo.Triple) bool {
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
// its subject or object, none of which was handed to the sink, for
// LinkCopiedFiles to rewrite and hand on.
func (p *TaskOutputParser) FileTriples() []rdflibgo.Triple { return p.fileTriples }

// Vocabularies returns, sorted, the namespaces the output's triples are
// stated in, which the build pins so that the vocabularies the table ends up
// holding terms of are the ones the project lists. A namespace counts when a
// @context binds a prefix (or a term, or @vocab) to it, the module ontology's
// context or one a node carried, and the output used a term of it as a
// predicate, as the class of a typed node, or as a literal's datatype; the
// namespace reported is the bound IRI, the longest one the term starts with,
// the way a source file's declared prefix is what gets pinned. A prefix bound
// without the output using it, sh for the shapes of the module's ontology
// say, is not among them, nor is a namespace that only names subjects or
// objects, since those are instances rather than vocabulary, nor one the
// output only ever wrote terms of as full IRIs: writing a full IRI is a
// reference to something, the way a Turtle file writing one is, while
// binding a prefix declares that the output is stated in that vocabulary. A
// harvested node typed with a class of an ontology the module never bound,
// a KnowWhereGraph S2 cell in Geoconnex data say, is left to the project to
// declare if it wants it pinned.
func (p *TaskOutputParser) Vocabularies() []string {
	namespaces := map[string]bool{}
	for term := range p.terms {
		if namespace := p.boundNamespace(term); namespace != "" {
			namespaces[namespace] = true
		}
	}
	sorted := make([]string, 0, len(namespaces))
	for namespace := range namespaces {
		sorted = append(sorted, namespace)
	}
	sort.Strings(sorted)
	return sorted
}

// boundNamespace is the longest IRI a context bound that term starts with,
// or "" when no binding covers it.
func (p *TaskOutputParser) boundNamespace(term string) string {
	longest := ""
	for binding := range p.bindings {
		if len(binding) > len(longest) && strings.HasPrefix(term, binding) {
			longest = binding
		}
	}
	return longest
}

// noteContexts records into bindings the namespaces every @context the
// decoded JSON value carries binds, at its top or on any node nested in it,
// since JSON-LD lets an embedded node carry a context of its own.
func noteContexts(bindings map[string]bool, value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, nested := range value {
			if key == "@context" {
				noteBindings(bindings, nested)
			} else {
				noteContexts(bindings, nested)
			}
		}
	case []any:
		for _, element := range value {
			noteContexts(bindings, element)
		}
	}
}

// noteBindings records into bindings every namespace one decoded @context
// binds: the value of a prefix or @vocab entry as it is, the namespace of the
// @id of an expanded term definition, since that declares one term of a
// vocabulary, and the bindings of a scoped context inside one. A keyword
// alias ("type": "@type") binds nothing, and a remote context, a string in
// place of an object, is not fetched. A keyword or a relative name is not a
// binding.
func noteBindings(bindings map[string]bool, context any) {
	note := func(iri string) {
		if !strings.HasPrefix(iri, "@") && strings.Contains(iri, ":") {
			bindings[iri] = true
		}
	}
	switch context := context.(type) {
	case []any:
		for _, element := range context {
			noteBindings(bindings, element)
		}
	case map[string]any:
		for key, value := range context {
			if strings.HasPrefix(key, "@") && key != "@vocab" {
				continue
			}
			switch value := value.(type) {
			case string:
				note(value)
			case map[string]any:
				if id, ok := value["@id"].(string); ok {
					note(VocabularyNamespace(id))
				}
				if scoped, ok := value["@context"]; ok {
					noteBindings(bindings, scoped)
				}
			}
		}
	}
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

// noteTerm records a term the output used as a predicate, class, or datatype.
func (p *TaskOutputParser) noteTerm(iri string) { p.terms[iri] = true }

// dispatch hands the pending batch to a parse worker and then hands on, in
// order, every batch at the head of the queue that has been parsed, waiting
// for the oldest when twice as many batches as there are workers are in
// flight, which bounds the memory the batches waiting on it hold.
func (p *TaskOutputParser) dispatch() error {
	if p.pendingLines > 0 {
		p.pending.WriteString("]}")
		batch := taskOutputBatch{document: p.pending.Bytes(), contexts: p.pendingContexts, first: p.firstPendingLine, last: p.lastLine}
		p.pending = bytes.Buffer{}
		p.pending.Write(p.head)
		p.pendingLines, p.pendingContexts = 0, nil
		result := make(chan parsedBatch, 1)
		p.inflight = append(p.inflight, result)
		go func() {
			p.workers <- struct{}{}
			defer func() { <-p.workers }()
			result <- p.parseBatch(batch)
		}()
	}
	for len(p.inflight) > 0 {
		var parsed parsedBatch
		if len(p.inflight) > 2*cap(p.workers) {
			parsed = <-p.inflight[0]
		} else {
			select {
			case parsed = <-p.inflight[0]:
			default:
				return nil
			}
		}
		p.inflight = p.inflight[1:]
		if p.failed = p.deliver(parsed); p.failed != nil {
			return p.failed
		}
	}
	return nil
}

// parseBatch parses one batch as one JSON-LD document into its triples, its
// blank nodes relabelled a connected component at a time (see
// TaskOutputParser) in place of the _:b0, _:b1, ... the JSON-LD processor
// gives every document, and decodes the @context its lines carry. It runs on
// a parse worker, so it reads nothing of the parser that changes.
func (p *TaskOutputParser) parseBatch(batch taskOutputBatch) parsedBatch {
	graph := rdflibgo.NewGraph(rdflibgo.WithBase(p.base))
	if err := jsonld.Parse(context.Background(), graph, bytes.NewReader(batch.document), jsonld.WithBase(p.base), jsonld.WithUnboundedLines()); err != nil {
		return parsedBatch{err: fmt.Errorf("parse %s lines %d-%d of SAL module %s: %w", p.stream, batch.first, batch.last, p.ontology.Namespace, err)}
	}
	triples := make([]rdflibgo.Triple, 0, pkg.GraphLen(graph))
	pkg.Triples(graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		triples = append(triples, triple)
		return true
	})
	pkg.StableBlankNodeComponents(triples)

	bindings := map[string]bool{}
	for _, at := range batch.contexts {
		var node any
		if err := json.Unmarshal(batch.document[at[0]:at[1]], &node); err == nil {
			noteContexts(bindings, node)
		}
	}
	return parsedBatch{triples: triples, bindings: bindings}
}

// deliver hands the triples of one parsed batch to the sink, noting the
// terms they use and the salmodule:Error nodes among them, and holding back
// those naming a file:/// IRI.
func (p *TaskOutputParser) deliver(parsed parsedBatch) error {
	if parsed.err != nil {
		return parsed.err
	}
	for binding := range parsed.bindings {
		p.bindings[binding] = true
	}
	for _, triple := range parsed.triples {
		p.noteTerm(triple.Predicate.Value())
		if triple.Predicate.Equal(rdflibgo.RDF.Type) {
			if class, ok := triple.Object.(rdflibgo.URIRef); ok {
				p.noteTerm(class.Value())
				if class.Value() == Namespace+"Error" {
					p.errorNodes[triple.Subject.String()] = triple.Subject
				}
			}
		}
		if literal, ok := triple.Object.(rdflibgo.Literal); ok {
			p.noteTerm(literal.Datatype().Value())
		}
		if isFileIRI(triple.Subject) || isFileIRI(triple.Object) {
			p.fileTriples = append(p.fileTriples, triple)
			continue
		}
		if err := p.sink(triple); err != nil {
			return err
		}
	}
	// the error nodes' own statements are what Finish reports them with, so
	// they are kept when the output itself is handed on rather than kept
	if p.errorsApart && len(p.errorNodes) > 0 {
		for _, triple := range parsed.triples {
			if _, ok := p.errorNodes[triple.Subject.String()]; ok {
				pkg.AddTriple(p.errors, triple.Subject, triple.Predicate, triple.Object)
			}
		}
	}
	return nil
}

// isRDFSPredicate reports whether an IRI expresses the rdfs term named, label
// or comment. Module ontologies do not always bind the rdfs prefix in their
// @context, in which case the JSON-LD processor keeps the term verbatim as
// "rdfs:comment".
func isRDFSPredicate(iri string, term string) bool {
	return iri == "http://www.w3.org/2000/01/rdf-schema#"+term || strings.HasSuffix(iri, ":"+term)
}
