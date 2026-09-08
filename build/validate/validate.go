package validate

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
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
}

// Validator checks RDF files against the vocabulary versions a project pins.
// One validator serves a whole run, so a vocabulary is resolved once no matter
// how many files use it and so every prefix the files declared can be pinned
// once they have all been read.
type Validator struct {
	vocabs vocabularyCache
	// declared is every prefix namespace the validated files declared, whether
	// or not a term from it was used, mapped to the files that declared it in
	// the order they were validated
	declared map[string][]string
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
		declared: map[string][]string{},
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
		v.declare(doc.ctx.Vocab, path)
	}
	for _, namespace := range doc.ctx.Prefixes {
		v.declare(namespace, path)
	}
	return &Document{path: path, doc: doc}
}

// Validate checks that every vocabulary term a parsed document uses is defined
// by the vocabulary declared for its prefix, and returns the document's graph.
func (v *Validator) Validate(doc *Document) (*rdflibgo.Graph, error) {
	if err := v.validateTerms(doc.path, doc.doc.terms, doc.doc.ctx); err != nil {
		return nil, err
	}
	return doc.doc.graph, nil
}

// ValidateFile parses one file and checks its terms in one step.
func (v *Validator) ValidateFile(path string) (*rdflibgo.Graph, error) {
	doc, err := v.ParseFile(path)
	if err != nil {
		return nil, err
	}
	return v.Validate(doc)
}

func (v *Validator) declare(namespace, path string) {
	if !slices.Contains(v.declared[namespace], path) {
		v.declared[namespace] = append(v.declared[namespace], path)
	}
}

// declaredPrefixes lists every declared namespace with the prefix maps applied,
// so the checks see the namespaces a build resolves against rather than the
// ones written in the files, sorted so reports and prompts come in a stable
// order. Two declarations one prefix map folds together are listed once.
func (v *Validator) declaredPrefixes() []DeclaredPrefix {
	byNamespace := map[string]*DeclaredPrefix{}
	for declared, paths := range v.declared {
		namespace := replacementVocabularyBase(declared, v.vocabs.replacements)
		prefix, ok := byNamespace[namespace]
		if !ok {
			prefix = &DeclaredPrefix{Namespace: namespace}
			byNamespace[namespace] = prefix
		}
		for _, path := range paths {
			if !slices.Contains(prefix.Paths, path) {
				prefix.Paths = append(prefix.Paths, path)
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
func (v *Validator) PinDeclaredPrefixes() error {
	var errs MultiError
	for _, prefix := range v.declaredPrefixes() {
		namespace := prefix.Namespace
		if !v.vocabs.resolvable(namespace) {
			continue
		}
		if _, err := v.vocabs.load(namespace); err != nil {
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
func ValidateRDFFile(path string, vocabsToReplace map[string]string, base string) (*rdflibgo.Graph, error) {
	return NewValidator(EphemeralVocabularies(), base, vocabsToReplace).ValidateFile(path)
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

func (v *Validator) validateTerms(path string, terms []UsedTermsInFile, rdfPrefixes RdfContext) error {
	vocabs := &v.vocabs

	var errs MultiError
	loggedVocabularyErrors := map[string]bool{}
	for _, term := range terms {
		display, ok := displayTerm(term.iri, rdfPrefixes)
		if !ok {
			continue
		}
		defined, err := vocabs.isDefined(term.iri, rdfPrefixes)
		if err != nil {
			logKey := term.iri + "\x00" + err.Error()
			if !loggedVocabularyErrors[logKey] {
				slog.Error("Failed to check vocabulary definition", "path", path, "term", term.iri, "error", err)
				loggedVocabularyErrors[logKey] = true
			}
			errs = append(errs, vocabularyLookupError{Path: path, Line: term.line, Term: display, Err: err})
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
