package validate

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/pkg/telemetry"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel/attribute"
)

type RdfContext struct {
	Prefixes map[string]string
	// The vocab is the implicit prefix
	Vocab string
}

type Vocabulary struct {
	terms map[string]bool
}

type UsedTermsInFile struct {
	iri  string
	line int
}

type rdfDocument struct {
	graph *rdflibgo.Graph
	ctx   RdfContext
	terms []UsedTermsInFile
	// content is the source the document was parsed from, kept so that a
	// prefix declaration can be reported with the line it is on
	content []byte
}

// Location is a place in a source file: the file and a line in it, or the
// whole file when the line is 0.
type Location struct {
	Path string
	Line int
}

func (l Location) String() string {
	if l.Line == 0 {
		return l.Path
	}
	return fmt.Sprintf("%s:%d", l.Path, l.Line)
}

// declarationLine is the line a namespace is declared on: the first line that
// writes it as an IRI, in angle brackets in Turtle or quoted in JSON-LD. It is
// 0 when the namespace is nowhere in the file, as when a prefix map wrote it.
func declarationLine(content []byte, namespace string) int {
	for _, written := range []string{"<" + namespace + ">", `"` + namespace + `"`} {
		if i := bytes.Index(content, []byte(written)); i >= 0 {
			return jsonOffsetLine(content, int64(i))
		}
	}
	return 0
}

// Validator checks RDF files against the vocabulary versions a project pins.
// One validator serves a whole run, so a vocabulary is resolved once no matter
// how many files use it and so every prefix the files declared can be pinned
// once they have all been read.
type Validator struct {
	vocabs vocabularyCache
	// declared is every prefix namespace the validated files declared, whether
	// or not a term from it was used, mapped to where it was declared in the
	// order the files were validated
	declared map[string][]Location
}

// DeclaredPrefix is a prefix namespace the validated files declared and the
// files that declared it.
type DeclaredPrefix struct {
	Namespace string
	Paths     []string
}

// NewValidator returns a validator that resolves vocabularies through pins.
// Pass EphemeralVocabularies to resolve them without recording anything.
func NewValidator(pins *PinnedVocabularies, base string, vocabsToReplace map[string]string) *Validator {
	return &Validator{
		vocabs: vocabularyCache{
			pins:         pins,
			cache:        map[string]Vocabulary{},
			failures:     map[string]error{},
			replacements: vocabsToReplace,
			base:         base,
		},
		declared: map[string][]Location{},
	}
}

// Document is a parsed source file whose declared prefixes the validator has
// recorded and whose terms have not been checked yet. Parsing every file before
// checking any terms is what lets the declared prefixes be inspected, and the
// user asked about them, before a single vocabulary is resolved.
type Document struct {
	path string
	doc  *rdfDocument
}

// ParseFile parses a Turtle or JSON-LD file and records the prefixes it
// declares. Check its terms with Validate.
func (v *Validator) ParseFile(path string) (*Document, error) {
	var (
		doc *rdfDocument
		err error
	)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ttl", ".turtle":
		doc, err = parseTurtleFile(path, v.vocabs.base)
	default:
		doc, err = parseJSONLDFile(path, v.vocabs.base)
	}
	if err != nil {
		return nil, err
	}
	return v.parsed(path, doc), nil
}

// ParseContent parses in-memory JSON-LD content the same way ParseFile parses
// a JSON-LD file on disk, reporting errors against displayPath. build uses it
// for the project ontology node .sal/config.jsonld carries, which is not a
// file of its own to pass ParseFile.
func (v *Validator) ParseContent(content []byte, displayPath string) (*Document, error) {
	doc, err := parseJSONLDContent(content, displayPath, v.vocabs.base)
	if err != nil {
		return nil, err
	}
	return v.parsed(displayPath, doc), nil
}

func (v *Validator) parsed(path string, doc *rdfDocument) *Document {
	if doc.ctx.Vocab != "" {
		v.declare(doc.ctx.Vocab, path, doc.content)
	}
	for _, namespace := range doc.ctx.Prefixes {
		v.declare(namespace, path, doc.content)
	}
	return &Document{path: path, doc: doc}
}

// Validate checks that every vocabulary term a parsed document uses is defined
// by the vocabulary declared for its prefix, and returns the document's graph.
func (v *Validator) Validate(ctx context.Context, doc *Document) (_ *rdflibgo.Graph, err error) {
	ctx, span := telemetry.Start(ctx, "validate.document", attribute.String("sal.file", doc.path), attribute.Int("sal.terms", len(doc.doc.terms)))
	defer func() { telemetry.End(span, err) }()
	if err := v.validateTerms(ctx, doc.path, doc.doc.terms, doc.doc.ctx); err != nil {
		return nil, err
	}
	return doc.doc.graph, nil
}

// ValidateFile parses one file and checks its terms in one step.
func (v *Validator) ValidateFile(ctx context.Context, path string) (*rdflibgo.Graph, error) {
	doc, err := v.ParseFile(path)
	if err != nil {
		return nil, err
	}
	return v.Validate(ctx, doc)
}

func (v *Validator) declare(namespace, path string, content []byte) {
	if !slices.ContainsFunc(v.declared[namespace], func(l Location) bool { return l.Path == path }) {
		v.declared[namespace] = append(v.declared[namespace], Location{Path: path, Line: declarationLine(content, namespace)})
	}
}

// declaredPrefixes lists every declared namespace with the prefix maps applied,
// so the checks see the namespaces a build resolves against rather than the
// ones written in the files, sorted so reports and prompts come in a stable
// order. Two declarations one prefix map folds together are listed once.
func (v *Validator) declaredPrefixes() []DeclaredPrefix {
	byNamespace := map[string]*DeclaredPrefix{}
	for declared, locations := range v.declared {
		namespace := replacementVocabularyBase(declared, v.vocabs.replacements)
		prefix, ok := byNamespace[namespace]
		if !ok {
			prefix = &DeclaredPrefix{Namespace: namespace}
			byNamespace[namespace] = prefix
		}
		for _, location := range locations {
			if !slices.Contains(prefix.Paths, location.Path) {
				prefix.Paths = append(prefix.Paths, location.Path)
			}
		}
	}

	prefixes := make([]DeclaredPrefix, 0, len(byNamespace))
	for _, prefix := range byNamespace {
		sort.Strings(prefix.Paths)
		prefixes = append(prefixes, *prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].Namespace < prefixes[j].Namespace })
	return prefixes
}

// PrefixesWithoutTerminator reports every declared prefix whose namespace ends
// in neither a `/` nor a `#`. Such a namespace is almost always a mistake, since
// its terms concatenate straight onto it (`foo.comname` rather than
// `foo.com/name`), but it is not invalid RDF, so the caller decides whether to
// include it rather than this package rejecting it outright.
func (v *Validator) PrefixesWithoutTerminator() []DeclaredPrefix {
	var suspicious []DeclaredPrefix
	for _, prefix := range v.declaredPrefixes() {
		if strings.HasSuffix(prefix.Namespace, "/") || strings.HasSuffix(prefix.Namespace, "#") {
			continue
		}
		suspicious = append(suspicious, prefix)
	}
	return suspicious
}

// httpsOnlyNamespaces maps the http spelling of a namespace whose vocabulary is
// only defined under https to the https namespace. schema.org defines every
// term under https://schema.org/, so a term written against the http namespace
// names nothing the vocabulary defines, however familiar it looks.
var httpsOnlyNamespaces = map[string]string{
	"http://" + strings.TrimPrefix(schemaOrgNamespace, "https://"): schemaOrgNamespace,
}

// InsecurePrefixError is one declaration of a namespace under http when the
// vocabulary only defines its terms under https.
type InsecurePrefixError struct {
	// Location is where the namespace is declared
	Location Location
	// Namespace is the http namespace as declared
	Namespace string
	// HTTPSNamespace is the namespace the vocabulary defines its terms under
	HTTPSNamespace string
}

func (e InsecurePrefixError) Error() string {
	vocabulary := strings.TrimSuffix(strings.TrimPrefix(e.HTTPSNamespace, "https://"), "/")
	return fmt.Sprintf("%s: http used instead of https for %s; declare <%s> instead", e.Location, vocabulary, e.HTTPSNamespace)
}

// InsecurePrefixes reports every declaration of a namespace under http when the
// vocabulary only defines its terms under https, such as `http://schema.org/`,
// one error per file declaring it. The prefix maps are applied first, so a
// file that cannot be edited is fixed by mapping the http namespace onto the
// https one.
func (v *Validator) InsecurePrefixes() []InsecurePrefixError {
	var errs []InsecurePrefixError
	for declared, locations := range v.declared {
		namespace := replacementVocabularyBase(declared, v.vocabs.replacements)
		secure, ok := httpsOnlyNamespaces[namespace]
		if !ok {
			continue
		}
		for _, location := range locations {
			errs = append(errs, InsecurePrefixError{Location: location, Namespace: namespace, HTTPSNamespace: secure})
		}
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].Location.Path != errs[j].Location.Path {
			return errs[i].Location.Path < errs[j].Location.Path
		}
		return errs[i].Location.Line < errs[j].Location.Line
	})
	return errs
}

// PrefixConflict is one vocabulary the validated files declare under more than
// one namespace, mixing http with https or a namespace with and without its
// trailing `/` or `#`.
type PrefixConflict struct {
	// Vocabulary names the vocabulary without a scheme or trailing / or #, the
	// part every spelling shares
	Vocabulary string
	// MixesSchemes is whether the spellings include both http and https
	MixesSchemes bool
	// MixesTerminators is whether the spellings differ by a trailing / or #
	MixesTerminators bool
	// Spellings are the namespaces declared for the vocabulary, each with the
	// files declaring it
	Spellings []DeclaredPrefix
}

// ConflictingPrefixes reports every vocabulary declared under namespaces that
// differ only by http/https or by a trailing `/` or `#`, such as
// `http://schema.org/` beside `https://schema.org/`. Terms from the two never
// compare equal, so the same vocabulary would end up in the table under two
// IRIs; a project must pick one, or fold them together with --prefix-maps.
func (v *Validator) ConflictingPrefixes() []PrefixConflict {
	byKey := map[string][]DeclaredPrefix{}
	var keys []string
	for _, prefix := range v.declaredPrefixes() {
		key := strings.TrimPrefix(strings.TrimPrefix(prefix.Namespace, "https://"), "http://")
		key = strings.TrimRight(key, "/#")
		if _, seen := byKey[key]; !seen {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], prefix)
	}

	var conflicts []PrefixConflict
	for _, key := range keys {
		spellings := byKey[key]
		if len(spellings) < 2 {
			continue
		}
		conflict := PrefixConflict{Vocabulary: key, Spellings: spellings}
		schemes := map[string]bool{}
		withoutScheme := map[string]bool{}
		for _, spelling := range spellings {
			scheme, rest, _ := strings.Cut(spelling.Namespace, "://")
			schemes[scheme] = true
			withoutScheme[rest] = true
		}
		conflict.MixesSchemes = len(schemes) > 1
		conflict.MixesTerminators = len(withoutScheme) > 1
		conflicts = append(conflicts, conflict)
	}
	return conflicts
}

// PinDeclaredPrefixes resolves the vocabulary behind every prefix the validated
// files declared, so a project pins what it declares rather than only what it
// happened to use. A prefix that cannot be resolved fails the run, since a
// build cannot pin a version of a vocabulary it cannot read.
func (v *Validator) PinDeclaredPrefixes(ctx context.Context) error {
	var errs MultiError
	for _, prefix := range v.declaredPrefixes() {
		namespace := prefix.Namespace
		if !v.vocabs.resolvable(namespace) {
			continue
		}
		if _, err := v.vocabs.load(ctx, namespace); err != nil {
			errs = append(errs, fmt.Errorf("failed to resolve the vocabulary declared for prefix <%s>: %w", namespace, err))
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ValidateRDFFile checks one file's terms without pinning the vocabularies it
// resolved. A build validates through a Validator holding the project's pins.
func ValidateRDFFile(ctx context.Context, path string, vocabsToReplace map[string]string, base string) (*rdflibgo.Graph, error) {
	return NewValidator(EphemeralVocabularies(), base, vocabsToReplace).ValidateFile(ctx, path)
}

func displayTerm(iri string, ctx RdfContext) (string, bool) {
	prefix, base, ok := longestPrefixBase(iri, ctx)
	if ok && prefix != "" {
		return prefix + ":" + strings.TrimPrefix(iri, base), true
	}
	if ok {
		return iri, true
	}
	for prefix, base := range ctx.Prefixes {
		if strings.HasPrefix(iri, base) {
			return prefix + ":" + strings.TrimPrefix(iri, base), true
		}
	}
	return iri, true
}

func (v *Validator) validateTerms(ctx context.Context, path string, terms []UsedTermsInFile, rdfPrefixes RdfContext) error {
	vocabs := &v.vocabs

	var errs MultiError
	// A vocabulary that cannot be checked at all, because its document failed
	// to fetch or parse, fails every term used from it in the same way. That is
	// one problem, so it is reported once, at the first use, with the other
	// lines it affects listed, rather than once per use.
	lookupFailures := map[string]*vocabularyLookupError{}
	for _, term := range terms {
		display, ok := displayTerm(term.iri, rdfPrefixes)
		if !ok {
			continue
		}
		defined, err := vocabs.isDefined(ctx, term.iri, rdfPrefixes)
		if err != nil {
			if failure, seen := lookupFailures[err.Error()]; seen {
				if term.line != failure.Line && !slices.Contains(failure.OtherLines, term.line) {
					failure.OtherLines = append(failure.OtherLines, term.line)
				}
				continue
			}
			failure := &vocabularyLookupError{Path: path, Line: term.line, Term: display, Err: err}
			lookupFailures[err.Error()] = failure
			errs = append(errs, failure)
			continue
		}
		if defined {
			continue
		}
		errs = append(errs, validationError{Path: path, Line: term.line, Term: display})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}
