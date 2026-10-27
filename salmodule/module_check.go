package salmodule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/specs"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/shacl"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// OntologyFile and DockerfileName are the two files a SAL module repository
// holds in its root: the document its container prints in response to
// `salmodule ontology`, and the Dockerfile sal builds that container from.
const (
	OntologyFile   = "ontology.jsonld"
	DockerfileName = "Dockerfile"
)

// IsModuleRoot reports whether dir holds both files a SAL module needs,
// ontology.jsonld and a Dockerfile.
func IsModuleRoot(dir string) bool {
	for _, name := range []string{OntologyFile, DockerfileName} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// ModuleError is one thing wrong with a SAL module, at a line of the file it
// is in. Line is 0 when the problem is the file as a whole, such as a missing
// Dockerfile.
type ModuleError struct {
	Path    string
	Line    int
	Message string
}

func (e ModuleError) Error() string {
	if e.Line == 0 {
		return e.Path + ": " + e.Message
	}
	return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Message)
}

// CheckModule checks the SAL module in dir without building or running
// anything: that it has a Dockerfile and an ontology.jsonld, that the ontology
// has the structure the SAL Module specification requires, that every term it
// refers to in its own namespace is declared in it, and that every SHACL shape
// it declares is a well-formed shape sal can enforce. namespace is the
// module's own salmodule:// namespace, which its relative terms resolve
// against. It returns one ModuleError per problem, each naming the file and
// the line, sorted by line.
func CheckModule(dir string, namespace string) []error {
	var problems []error
	dockerfile := filepath.Join(dir, DockerfileName)
	if info, err := os.Stat(dockerfile); err != nil || info.IsDir() {
		problems = append(problems, ModuleError{Path: dockerfile, Message: "a SAL module needs a Dockerfile in its root; sal builds it and runs the module's commands inside the resulting image"})
	}
	path := filepath.Join(dir, OntologyFile)
	content, err := os.ReadFile(path)
	if err != nil {
		return append(problems, ModuleError{Path: path, Message: "a SAL module needs an ontology.jsonld in its root, the document its container prints in response to `" + BaseCommand + " " + OntologyCommand + "`"})
	}
	return append(problems, CheckOntologyDocument(path, content, namespace)...)
}

// CheckOntologyDocument runs every check CheckModule makes on the ontology
// document itself, reporting against path. The JSON Schema in specs/ is what
// decides the structure, so the rules an editor applies while the document is
// written are the ones sal applies here; the checks after it need the RDF the
// document expands to, which the schema cannot see. `sal init --salmodule
// --ui` runs it over the document its form assembles before writing it, so
// the form refuses exactly what `sal validate --salmodule` would.
func CheckOntologyDocument(path string, content []byte, namespace string) []error {
	source, err := scanOntologySource(content)
	if err != nil {
		return []error{ModuleError{Path: path, Line: jsonErrorLine(content, err), Message: "invalid JSON: " + err.Error()}}
	}
	var problems []error
	dropped := map[string]bool{}
	for _, key := range source.relativeKeys {
		dropped[key.name] = true
		problems = append(problems, ModuleError{Path: path, Line: key.line, Message: fmt.Sprintf("key %q is neither a prefixed term nor a term the @context defines, so JSON-LD drops it and its value; write it as a prefixed term such as schema:%s, or define it in @context", key.name, key.name)})
	}
	// the schema refuses such a key inside a shape too, on the line of the
	// shape rather than of the key, so the key is reported once
	for _, problem := range schemaProblems(path, content, source.lines) {
		if key, ok := droppedKeyNamed(problem.(ModuleError).Message); ok && dropped[key] {
			continue
		}
		problems = append(problems, problem)
	}

	ontology, err := parseModuleOntology(namespace, content)
	if err != nil {
		return append(problems, ModuleError{Path: path, Line: 1, Message: "the document does not parse as JSON-LD: " + err.Error()})
	}
	checker := ontologyChecker{path: path, ontology: ontology, source: source}
	problems = append(problems, checker.undeclaredTerms()...)
	problems = append(problems, checker.shapeProblems(problems)...)

	sort.SliceStable(problems, func(i, j int) bool {
		a, b := problems[i].(ModuleError), problems[j].(ModuleError)
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Message < b.Message
	})
	return problems
}

// sourceScan is what one pass over the ontology's JSON tokens recovers: where
// every value is, so a schema violation can be reported on its line; where
// every term is written and declared, so a problem with a node can be; and
// the object keys JSON-LD would drop.
type sourceScan struct {
	// lines is the line each value starts on, keyed by the value's JSON pointer
	lines map[string]int
	// terms is the first line each @id or @type value is written on, as
	// written, so "region" for a relative term and "schema:name" for a
	// prefixed one
	terms map[string]int
	// declarations is the line of the node object declaring each @id: the one
	// carrying that @id and an @type, failing that one saying anything else
	// about it, failing that the first reference such as {"@id": "region"}
	declarations map[string]int
	// declaredWith is how much the object declarations records said: 2 for
	// an @type, 1 for other keys, 0 for a bare reference
	declaredWith map[string]int
	// relativeKeys are the object keys outside @context that are neither
	// JSON-LD keywords, nor prefixed terms, nor terms the @context defines, in
	// document order. A JSON-LD processor expands a key with no @vocab in scope
	// to nothing and drops it, so such a key never reaches the RDF.
	relativeKeys []relativeKey
}

type relativeKey struct {
	name string
	line int
}

// scanOntologySource walks the document's tokens once. The error it returns
// is the json syntax error, so the caller can place it.
func scanOntologySource(content []byte) (*sourceScan, error) {
	var envelope struct {
		Context json.RawMessage `json:"@context"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return nil, err
	}
	// a key the top-level @context defines as a term is kept by the processor,
	// whatever it looks like; a @context that is not an object defines none
	// the scan can see. Without any @context every key is dropped, which the
	// schema reports once rather than this scan once per key.
	var defined map[string]json.RawMessage
	if envelope.Context != nil {
		defined = map[string]json.RawMessage{}
		_ = json.Unmarshal(envelope.Context, &defined)
	}

	scan := &sourceScan{lines: map[string]int{}, terms: map[string]int{}, declarations: map[string]int{}, declaredWith: map[string]int{}}
	dec := json.NewDecoder(bytes.NewReader(content))
	_, err := scan.value(dec, content, "", defined == nil, false, defined)
	return scan, err
}

// value reads one JSON value and returns its first token. term is set for the
// value of an @id or @type key, whose strings name terms.
func (s *sourceScan) value(dec *json.Decoder, content []byte, pointer string, inContext bool, term bool, defined map[string]json.RawMessage) (json.Token, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	// a token never spans lines, so the line after it is the line it is on
	line := lineAt(content, dec.InputOffset())
	s.lines[pointer] = line
	if name, ok := tok.(string); ok && term && !inContext {
		if _, seen := s.terms[name]; !seen {
			s.terms[name] = line
		}
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		var id string
		said := 0
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("expected an object key")
			}
			if !inContext && !strings.HasPrefix(key, "@") && !strings.Contains(key, ":") {
				if _, ok := defined[key]; !ok {
					s.relativeKeys = append(s.relativeKeys, relativeKey{name: key, line: lineAt(content, dec.InputOffset())})
				}
			}
			value, err := s.value(dec, content, pointer+"/"+escapePointerToken(key), inContext || key == "@context", key == "@id" || key == "@type", defined)
			if err != nil {
				return nil, err
			}
			switch key {
			case "@id":
				id, _ = value.(string)
			case "@type":
				said = 2
			default:
				said = max(said, 1)
			}
		}
		if id != "" && !inContext {
			if _, declared := s.declarations[id]; !declared || said > s.declaredWith[id] {
				s.declarations[id] = line
				s.declaredWith[id] = said
			}
		}
	case '[':
		for i := 0; dec.More(); i++ {
			if _, err := s.value(dec, content, pointer+"/"+strconv.Itoa(i), inContext, term, defined); err != nil {
				return nil, err
			}
		}
	}
	_, err = dec.Token()
	return tok, err
}

func escapePointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func unescapePointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}

// lineAt is the 1-based line of the byte at offset.
func lineAt(content []byte, offset int64) int {
	if offset <= 0 {
		return 1
	}
	return 1 + bytes.Count(content[:min(int(offset), len(content))], []byte("\n"))
}

// jsonErrorLine is the line a json syntax error points at, or 1 when the
// error carries no offset.
func jsonErrorLine(content []byte, err error) int {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return lineAt(content, syntaxErr.Offset)
	}
	return 1
}

// moduleSchema compiles the SAL module ontology JSON Schema once per process
// and keeps the schema document itself, since the messages it annotates its
// rules with (errorMessage and patternErrorMessage, which an editor shows in
// place of the generic ones) are not something the compiled schema exposes.
var moduleSchema = sync.OnceValues(func() (*jsonschema.Schema, map[string]any) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(specs.SalModuleOntologySchema))
	if err != nil {
		panic("salmodule: the embedded module ontology schema is not JSON: " + err.Error())
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(specs.SalModuleOntologySchemaFile, document); err != nil {
		panic("salmodule: register the module ontology schema: " + err.Error())
	}
	schema, err := compiler.Compile(specs.SalModuleOntologySchemaFile)
	if err != nil {
		panic("salmodule: compile the module ontology schema: " + err.Error())
	}
	raw, _ := document.(map[string]any)
	return schema, raw
})

var schemaMessagePrinter = message.NewPrinter(language.English)

// schemaProblems checks the document against the SAL module ontology JSON
// Schema and reports each violation on the line of the value that violates
// it, with the message the schema annotates the rule with when it has one.
func schemaProblems(path string, content []byte, lines map[string]int) []error {
	schema, raw := moduleSchema()
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil {
		return []error{ModuleError{Path: path, Line: jsonErrorLine(content, err), Message: "invalid JSON: " + err.Error()}}
	}
	var violation *jsonschema.ValidationError
	if err := schema.Validate(instance); !errors.As(err, &violation) {
		return nil
	}
	var problems []error
	seen := map[string]bool{}
	for _, found := range schemaViolations(violation, raw, "") {
		problem := ModuleError{Path: path, Line: lines[found.instance], Message: found.message}
		if !seen[problem.Error()] {
			seen[problem.Error()] = true
			problems = append(problems, problem)
		}
	}
	return problems
}

// schemaViolation is one violation placed at the JSON pointer of the value it
// is about. wrongType is set when the violation is a value of the wrong JSON
// type, the least telling reason a form can fail to match.
type schemaViolation struct {
	instance  string
	message   string
	wrongType *kind.Type
}

// schemaViolations flattens the tree of causes behind a validation error into
// the violations worth reporting. A rule the schema annotates with a message
// is reported with that message in place of whatever failed beneath it. A
// choice between forms (anyOf, oneOf) without one reports the form that
// failed deepest, since that is the one the author was writing, rather than
// every form's reason for not matching; forms failing equally deep are all
// reported, except that wrong-type reasons give way to any other and are
// folded into one. parent is the pointer of the enclosing value, which a
// rule about an object's keys is reported against since the validator
// places such a violation nowhere.
func schemaViolations(err *jsonschema.ValidationError, raw map[string]any, parent string) []schemaViolation {
	instance := jsonPointer(err.InstanceLocation)
	if instance == "" {
		instance = parent
	}
	if message := annotatedMessage(schemaNode(raw, err.SchemaURL), err.ErrorKind); message != "" {
		return []schemaViolation{{instance: instance, message: message}}
	}
	if len(err.Causes) == 0 {
		violation := schemaViolation{instance: instance, message: err.ErrorKind.LocalizedString(schemaMessagePrinter)}
		violation.wrongType, _ = err.ErrorKind.(*kind.Type)
		return []schemaViolation{violation}
	}
	switch errorKind := err.ErrorKind.(type) {
	case *kind.PropertyNames:
		var violations []schemaViolation
		for _, cause := range err.Causes {
			for _, violation := range schemaViolations(cause, raw, instance) {
				violation.message = fmt.Sprintf("key %q: %s", errorKind.Property, violation.message)
				violations = append(violations, violation)
			}
		}
		return violations
	case *kind.AnyOf, *kind.OneOf:
		var best []schemaViolation
		bestRank := -1
		for _, cause := range err.Causes {
			violations := schemaViolations(cause, raw, instance)
			// twice the depth, plus one when some reason is not a wrong type
			rank := 0
			for _, violation := range violations {
				rank = max(rank, 2*strings.Count(violation.instance, "/"))
			}
			for _, violation := range violations {
				if violation.wrongType == nil {
					rank++
					break
				}
			}
			switch {
			case rank > bestRank:
				best, bestRank = violations, rank
			case rank == bestRank:
				best = append(best, violations...)
			}
		}
		return foldWrongTypes(best)
	}
	var violations []schemaViolation
	for _, cause := range err.Causes {
		violations = append(violations, schemaViolations(cause, raw, instance)...)
	}
	return violations
}

// foldWrongTypes turns the wrong-type reasons the forms of a choice give for
// one value, "got number, want string" and "got number, want object", into
// the one reason "got number, want string or object". Anything else is kept.
func foldWrongTypes(violations []schemaViolation) []schemaViolation {
	var folded []schemaViolation
	byInstance := map[string]int{}
	for _, violation := range violations {
		if violation.wrongType == nil {
			folded = append(folded, violation)
			continue
		}
		if i, ok := byInstance[violation.instance]; ok && folded[i].wrongType.Got == violation.wrongType.Got {
			want := folded[i].wrongType.Want
			for _, w := range violation.wrongType.Want {
				if !slices.Contains(want, w) {
					want = append(want, w)
				}
			}
			folded[i].wrongType = &kind.Type{Got: violation.wrongType.Got, Want: want}
			folded[i].message = folded[i].wrongType.LocalizedString(schemaMessagePrinter)
			continue
		}
		byInstance[violation.instance] = len(folded)
		folded = append(folded, violation)
	}
	return folded
}

// droppedKeyNamed reads the key out of a violation about an object's keys.
func droppedKeyNamed(message string) (string, bool) {
	rest, ok := strings.CutPrefix(message, "key ")
	if !ok {
		return "", false
	}
	key, err := strconv.Unquote(rest[:strings.Index(rest, `": `)+1])
	return key, err == nil
}

// annotatedMessage is the message the schema gives for a rule failing at
// node, or "" when it gives none. The wrappers the validator groups errors
// under carry no rule of their own, so a message on their node belongs to the
// rules beneath them.
func annotatedMessage(node map[string]any, errorKind jsonschema.ErrorKind) string {
	if node == nil {
		return ""
	}
	switch errorKind.(type) {
	case *kind.Schema, *kind.Group:
		return ""
	case *kind.Pattern:
		if message, ok := node["patternErrorMessage"].(string); ok {
			return message
		}
	}
	message, _ := node["errorMessage"].(string)
	return message
}

// schemaNode finds the schema object a validation error's SchemaURL names,
// the fragment of the URL being a JSON pointer into the schema document.
func schemaNode(raw map[string]any, schemaURL string) map[string]any {
	_, fragment, _ := strings.Cut(schemaURL, "#")
	var node any = raw
	for _, token := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
		if fragment == "" {
			break
		}
		if unescaped, err := url.PathUnescape(token); err == nil {
			token = unescaped
		}
		token = unescapePointerToken(token)
		switch current := node.(type) {
		case map[string]any:
			node = current[token]
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(current) {
				return nil
			}
			node = current[i]
		default:
			return nil
		}
	}
	object, _ := node.(map[string]any)
	return object
}

func jsonPointer(tokens []string) string {
	var pointer strings.Builder
	for _, token := range tokens {
		pointer.WriteByte('/')
		pointer.WriteString(escapePointerToken(token))
	}
	return pointer.String()
}

// ontologyChecker holds the parsed ontology for the checks that need the RDF
// rather than the JSON, and the scan of the source that places their
// problems.
type ontologyChecker struct {
	path     string
	ontology *ModuleOntology
	source   *sourceScan
}

// lineOf is the line the node object declaring iri begins on, falling back
// to the first line the IRI is written on; 0 when it is written nowhere, as
// when a scoped context introduced it.
func (c *ontologyChecker) lineOf(iri string) int {
	for _, written := range []string{c.ontology.compact(iri), iri} {
		if line, ok := c.source.declarations[written]; ok {
			return line
		}
		if line, ok := c.source.terms[written]; ok {
			return line
		}
	}
	return 0
}

// undeclaredTerms reports every term in the module's own namespace the
// document refers to, as a subject, a property, a type, or a value, without
// declaring it: a node in @graph with that @id and an @type. A term the
// module uses but does not declare is one no project can read the meaning of
// and one `sal validate` will refuse in any project that references it.
func (c *ontologyChecker) undeclaredTerms() []error {
	namespace := c.ontology.Namespace
	declared := map[string]bool{}
	rdfType := rdflibgo.RDF.Type
	for triple := range pkg.Triples(c.ontology.graph, nil, &rdfType, nil) {
		if subject, ok := triple.Subject.(rdflibgo.URIRef); ok {
			declared[subject.Value()] = true
		}
	}

	undeclared := map[string]bool{}
	for triple := range pkg.Triples(c.ontology.graph, nil, nil, nil) {
		for _, term := range []rdflibgo.Term{triple.Subject, triple.Predicate, triple.Object} {
			iri, ok := term.(rdflibgo.URIRef)
			if !ok || declared[iri.Value()] {
				continue
			}
			// the namespace itself is the ontology node, "@id": "."
			if name, local := strings.CutPrefix(iri.Value(), namespace); local && name != "" {
				undeclared[iri.Value()] = true
			}
		}
	}

	iris := make([]string, 0, len(undeclared))
	for iri := range undeclared {
		iris = append(iris, iri)
	}
	sort.Strings(iris)
	var problems []error
	for _, iri := range iris {
		name := strings.TrimPrefix(iri, namespace)
		problems = append(problems, ModuleError{Path: c.path, Line: c.lineOf(iri), Message: fmt.Sprintf("undefined term %s: no node in @graph declares \"@id\": %q with an @type", name, name)})
	}
	return problems
}

// shapeTermPattern finds the IRI a SHACL shape error names, written the way
// the shacl package renders one.
var shapeTermPattern = regexp.MustCompile(`<([^>\s]+)>`)

// unsupportedConstraintPattern finds the constraint parameter a message about
// a salmodule:stdoutShape names, whether the schema's ("sal does not support
// sh:sparql in a salmodule:stdoutShape") or StdoutValidator's ("uses
// sh:sparql, which SAL does not support").
var unsupportedConstraintPattern = regexp.MustCompile(`(?:support|uses) sh:(\w+)`)

// shapeProblems reports every SHACL shape in the ontology that is not a
// well-formed shape, as the shacl package that enforces the shapes reads them,
// and every salmodule:stdoutShape that uses a constraint sal refuses when a
// task is run, so that a module author finds out before a project runs it.
// alreadyReported are the problems found so far, since the schema has
// already checked what is written inline: an ill-formed shape written inline,
// a blank node, is reported by the schema on the line it is written on and
// is only the schema's to report, and a constraint the schema refused is not
// reported a second time on the line of the class.
func (c *ontologyChecker) shapeProblems(alreadyReported []error) []error {
	shapes, err := shacl.NewGraphFromRDF(context.Background(), c.ontology.graph, c.ontology.Namespace)
	if err != nil {
		return []error{ModuleError{Path: c.path, Message: "read the SHACL shapes: " + err.Error()}}
	}
	// under WithStrictShapes an ill-formed shape is the error, every one of
	// them joined into one
	var reported []error
	if _, err := shacl.CompileShapes(shapes, shacl.WithStrictShapes()); err != nil {
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			reported = joined.Unwrap()
		} else {
			reported = []error{err}
		}
	}

	var problems []error
	seen := map[string]bool{}
	for _, err := range reported {
		message := strings.TrimPrefix(err.Error(), shacl.ErrIllFormedShape.Error()+": ")
		if seen[message] || strings.HasPrefix(message, "property shape _:") {
			continue
		}
		seen[message] = true
		var line int
		if named := shapeTermPattern.FindStringSubmatch(message); named != nil {
			line = c.lineOf(named[1])
		}
		problems = append(problems, ModuleError{Path: c.path, Line: line, Message: "invalid SHACL shape: " + message})
	}

	refused := map[string]bool{}
	for _, err := range alreadyReported {
		if match := unsupportedConstraintPattern.FindStringSubmatch(err.Error()); match != nil {
			refused[match[1]] = true
		}
	}
	classes := make([]string, 0, len(c.ontology.taskClasses))
	for class := range c.ontology.taskClasses {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		_, err := c.ontology.StdoutValidator(class, c.ontology.Namespace)
		if err == nil {
			continue
		}
		if match := unsupportedConstraintPattern.FindStringSubmatch(err.Error()); match != nil && refused[match[1]] {
			continue
		}
		problems = append(problems, ModuleError{Path: c.path, Line: c.lineOf(class), Message: err.Error()})
	}
	return problems
}
