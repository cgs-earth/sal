package sparql

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
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
	// Statements is what the pinned document says about each term, by local
	// name, for every term it says something about; see
	// validate.VocabularyTerms. The editor shows them for the term under the
	// caret.
	Statements map[string][]validate.TermStatement `json:"statements"`
	// Error is why the pinned document could not be read, in which case the
	// term lists are empty; the prefix can still be inserted.
	Error string `json:"error,omitempty"`
	// Builtin marks a vocabulary the query engine defines rather than the
	// project pins: the geof: functions and uom: units the translator
	// answers and the xsd: datatypes. It has no Version or Format.
	Builtin bool `json:"builtin,omitempty"`
}

// VocabularyListing is every vocabulary the project pins, sorted by ID, and
// after them the built-in ones.
type VocabularyListing struct {
	Vocabularies []Vocabulary `json:"vocabularies"`
}

// VocabularyRunner lists the pinned vocabularies and their terms, which the
// UI's SPARQL tab reads.
type VocabularyRunner interface {
	Vocabularies(ctx context.Context) (VocabularyListing, error)
}

// projectFiles returns the config file and blob directory the project's pins
// live in: those of the served data directory when there is one, else those
// of the SAL project the working directory belongs to. A data directory
// without a config.jsonld is an error, so that it is reported rather than
// read as a project that pins nothing.
func (r DuckDBRunner) projectFiles() (configPath string, blobDir string, err error) {
	if r.DataRoot != "" {
		configPath = filepath.Join(r.DataRoot, "config.jsonld")
		if _, err := os.Stat(configPath); err != nil {
			return "", "", fmt.Errorf("the data directory has no project config: %w", err)
		}
		return configPath, filepath.Join(r.DataRoot, "blobs"), nil
	}
	if configPath, err = pkg.SalConfigPath(); err != nil {
		return "", "", err
	}
	if blobDir, err = pkg.SalBlobsDir(); err != nil {
		return "", "", err
	}
	return configPath, blobDir, nil
}

// Vocabularies lists every vocabulary the project pins in .sal/config.jsonld
// with the terms its pinned document defines, read from the blob store and
// never fetched, and then the built-in ones. A remote table has no project on
// disk, so it lists only the built-ins, the same as a directory that is not a
// SAL project; the Stats tab's vocabulary listing shows nothing for either. A
// vocabulary whose document cannot be read is listed with its error rather
// than failing the listing, since the others are still worth completing from.
func (r DuckDBRunner) Vocabularies(_ context.Context) (VocabularyListing, error) {
	listing := VocabularyListing{Vocabularies: []Vocabulary{}}
	if isRemoteTablePath(r.TablePath) {
		return withBuiltinVocabularies(listing), nil
	}
	path, blobDir, err := r.projectFiles()
	if err != nil {
		if r.DataRoot != "" {
			slog.Warn("serving a data directory without a project config; pinned vocabularies are not listed (sal upload writes config.jsonld beside the data; re-upload, or copy .sal/config.jsonld into the data directory)", "error", err)
		} else {
			slog.Debug("not reporting project vocabularies", "error", err)
		}
		return withBuiltinVocabularies(listing), nil
	}
	pins, err := validate.LoadPinnedVocabularies(path, blobDir)
	if err != nil {
		return VocabularyListing{}, err
	}
	var imports []string
	if ontology := projectOntology(path, r.DataRoot != ""); ontology != nil {
		imports = ontology.Imports
	}
	for _, id := range pins.IDs() {
		vocabulary := Vocabulary{ID: id, Imported: slices.Contains(imports, id), Classes: []string{}, Properties: []string{}, Others: []string{}, Statements: map[string][]validate.TermStatement{}}
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
			maps.Copy(vocabulary.Statements, terms.Statements)
		}
		listing.Vocabularies = append(listing.Vocabularies, vocabulary)
	}
	return withBuiltinVocabularies(listing), nil
}

// withBuiltinVocabularies appends the vocabularies the query engine itself
// defines, so the editor completes and declares them whether or not the
// project pins anything: the geof: functions and uom: units geoFunctionSQL
// translates, and the xsd: datatypes, which validation accepts without a
// document and castSQL answers some of. One the project pins under the same
// namespace is left as pinned, since that document says more.
func withBuiltinVocabularies(listing VocabularyListing) VocabularyListing {
	for _, builtin := range builtinVocabularies() {
		pinned := slices.ContainsFunc(listing.Vocabularies, func(vocabulary Vocabulary) bool {
			return vocabulary.Namespace == builtin.Namespace
		})
		if !pinned {
			listing.Vocabularies = append(listing.Vocabularies, builtin)
		}
	}
	return listing
}

func builtinVocabularies() []Vocabulary {
	builtin := func(prefix, namespace string) Vocabulary {
		return Vocabulary{
			ID: namespace, Prefix: prefix, Namespace: namespace, Builtin: true,
			Classes: []string{}, Properties: []string{}, Others: []string{},
			Statements: map[string][]validate.TermStatement{},
		}
	}
	comment := func(vocabulary Vocabulary, name, text string) {
		vocabulary.Statements[name] = []validate.TermStatement{{Predicate: "rdfs:comment", Value: text}}
	}

	geof := builtin("geof", geofNamespace)
	for _, function := range geofFunctions {
		geof.Others = append(geof.Others, function.name)
		comment(geof, function.name, function.comment)
	}
	slices.Sort(geof.Others)

	uom := builtin("uom", uomNamespace)
	uom.Others = []string{"degree", "metre"}
	comment(uom, "degree", "Degrees of the stored longitude/latitude coordinates, a planar distance in that frame; the default when geof:distance is given no unit.")
	comment(uom, "metre", "Great-circle metres between the nearest points of the two geometries.")

	xsd := builtin("xsd", validate.XSDNamespace)
	xsd.Classes = append(xsd.Classes, validate.XSDDatatypes...)
	slices.Sort(xsd.Classes)
	for _, name := range []string{"string", "integer", "int", "long", "short", "byte", "nonNegativeInteger", "positiveInteger", "nonPositiveInteger", "negativeInteger", "unsignedLong", "unsignedInt", "unsignedShort", "unsignedByte", "decimal", "double", "float", "boolean", "dateTime", "date"} {
		comment(xsd, name, "An XML Schema datatype. xsd:"+name+"(?x) is also a constructor, a TRY_CAST that leaves ?x unbound when it cannot be converted.")
	}
	return []Vocabulary{geof, uom, xsd}
}
