package validate

import (
	"log/slog"
	"path/filepath"
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

func ValidateRDFFile(path string, vocabsToReplace map[string]string, base string) (*rdflibgo.Graph, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ttl", ".turtle":
		return validateTurtleFile(path, vocabsToReplace, base)
	default:
		return validateJSONLDFile(path, vocabsToReplace, base)
	}
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

func validateTerms(path string, terms []UsedTermsInFile, rdfPrefixes RdfContext, vocabsToReplace map[string]string, base string) error {
	vocabs := vocabularyCache{
		cacheDir: defaultVocabularyCacheDir(),
		cache:    map[string]Vocabulary{},
		failures: map[string]error{},
		base:     base,
	}

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
