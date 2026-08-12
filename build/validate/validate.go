package validate

import (
	"fmt"
	"log/slog"
	"path/filepath"
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
	// or not a term from it was used
	declared map[string]bool
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
		declared: map[string]bool{},
	}
}

// ValidateFile parses a Turtle or JSON-LD file and checks that every used
// vocabulary term is defined by the vocabulary declared for its prefix.
func (v *Validator) ValidateFile(path string) (*rdflibgo.Graph, error) {
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
	return v.validateDocument(path, doc)
}

// ValidateContent checks in-memory JSON-LD content the same way ValidateFile
// checks a JSON-LD file on disk, reporting term errors against displayPath.
// build uses it for the project ontology node .sal/config.jsonld carries,
// which is not a file of its own to pass ValidateFile.
func (v *Validator) ValidateContent(content []byte, displayPath string) (*rdflibgo.Graph, error) {
	doc, err := parseJSONLDContent(content, displayPath, v.vocabs.base)
	if err != nil {
		return nil, err
	}
	return v.validateDocument(displayPath, doc)
}

func (v *Validator) validateDocument(path string, doc *rdfDocument) (*rdflibgo.Graph, error) {
	if doc.ctx.Vocab != "" {
		v.declared[doc.ctx.Vocab] = true
	}
	for _, namespace := range doc.ctx.Prefixes {
		v.declared[namespace] = true
	}

	if err := v.validateTerms(path, doc.terms, doc.ctx); err != nil {
		return nil, err
	}
	return doc.graph, nil
}

// PinDeclaredPrefixes resolves the vocabulary behind every prefix the validated
// files declared, so a project pins what it declares rather than only what it
// happened to use. A prefix that cannot be resolved fails the run, since a
// build cannot pin a version of a vocabulary it cannot read.
func (v *Validator) PinDeclaredPrefixes() error {
	namespaces := make([]string, 0, len(v.declared))
	for namespace := range v.declared {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)

	var errs MultiError
	for _, namespace := range namespaces {
		namespace = replacementVocabularyBase(namespace, v.vocabs.replacements)
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
