package sparql

import (
	"context"
	"log/slog"
	"slices"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/pkg"
)

// Vocabulary is one vocabulary the project pins, with the terms its pinned
// document defines, as the SPARQL tab of `sal serve --with-ui` reads it: the
// Prefixes dialog inserts Prefix and Namespace as a PREFIX line, and the
// editor completes `Prefix:` names from the three term lists.
type Vocabulary struct {
	// ID is the IRI the pin was recorded under, which is how the Stats tab
	// lists the vocabulary.
	ID string `json:"id"`
	// Prefix is the name to declare Namespace under, from the document or by
	// convention; the dialog picks another when a query already binds it.
	Prefix    string `json:"prefix"`
	Namespace string `json:"namespace"`
	Version   string `json:"version"`
	Format    string `json:"format"`
	// Imported reports that the project ontology imports the vocabulary with
	// owl:imports, so its statements are in the table and not only validated
	// against.
	Imported bool `json:"imported"`
	// Classes, Properties, and Others are the local names under Namespace,
	// each sorted; see validate.VocabularyTerms.
	Classes    []string `json:"classes"`
	Properties []string `json:"properties"`
	Others     []string `json:"others"`
	// Error is why the pinned document could not be read, in which case the
	// term lists are empty; the prefix can still be inserted.
	Error string `json:"error,omitempty"`
}

// VocabularyListing is every vocabulary the project pins, sorted by ID.
type VocabularyListing struct {
	Vocabularies []Vocabulary `json:"vocabularies"`
}

// VocabularyRunner lists the pinned vocabularies and their terms, which the
// UI's SPARQL tab reads.
type VocabularyRunner interface {
	Vocabularies(ctx context.Context) (VocabularyListing, error)
}

// Vocabularies lists every vocabulary the project pins in .sal/config.jsonld
// with the terms its pinned document defines, read from the blob store and
// never fetched. A remote table has no project on disk, so it lists nothing,
// the same as the Stats tab's vocabulary listing; so does a directory that is
// not a SAL project. A vocabulary whose document cannot be read is listed
// with its error rather than failing the listing, since the others are still
// worth completing from.
func (r DuckDBRunner) Vocabularies(_ context.Context) (VocabularyListing, error) {
	listing := VocabularyListing{Vocabularies: []Vocabulary{}}
	if isRemoteTablePath(r.TablePath) {
		return listing, nil
	}
	path, err := pkg.SalConfigPath()
	if err != nil {
		slog.Debug("not reporting project vocabularies", "error", err)
		return listing, nil
	}
	blobDir, err := pkg.SalBlobsDir()
	if err != nil {
		slog.Debug("not reporting project vocabularies", "error", err)
		return listing, nil
	}
	pins, err := validate.LoadPinnedVocabularies(path, blobDir)
	if err != nil {
		return VocabularyListing{}, err
	}
	var imports []string
	if ontology := projectOntology(); ontology != nil {
		imports = ontology.Imports
	}
	for _, id := range pins.IDs() {
		vocabulary := Vocabulary{ID: id, Imported: slices.Contains(imports, id), Classes: []string{}, Properties: []string{}, Others: []string{}}
		terms, err := pins.Terms(id)
		if err != nil {
			slog.Warn("listing a pinned vocabulary without its terms", "vocabulary", id, "error", err)
			vocabulary.Error = err.Error()
			vocabulary.Namespace = id
			vocabulary.Prefix = validate.PrefixFor(id)
		} else {
			vocabulary.Prefix = terms.Prefix
			vocabulary.Namespace = terms.Namespace
			vocabulary.Version = terms.Version
			vocabulary.Format = terms.Format
			vocabulary.Classes = append(vocabulary.Classes, terms.Classes...)
			vocabulary.Properties = append(vocabulary.Properties, terms.Properties...)
			vocabulary.Others = append(vocabulary.Others, terms.Others...)
		}
		listing.Vocabularies = append(listing.Vocabularies, vocabulary)
	}
	return listing, nil
}
