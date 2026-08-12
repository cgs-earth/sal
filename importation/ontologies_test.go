package importation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
)

func pinnedVocabularyRaw(t *testing.T, id string, format string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"@id":            id,
		"@type":          "owl:Ontology",
		"owl:versionIRI": map[string]string{"@id": "urn:sha256:" + id},
		"dcterms:format": format,
	})
	require.NoError(t, err)
	return raw
}

func TestOntologyRowsMarksAPinThatIsAlsoImported(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := OntologyRows(pinned, []string{"https://schema.org/"})
	require.NoError(t, err)
	require.Equal(t, []OntologyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:https://schema.org/", Format: "application/ld+json", Imported: true},
	}, rows)
}

func TestOntologyRowsMarksAPinThatIsNotImported(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := OntologyRows(pinned, nil)
	require.NoError(t, err)
	require.False(t, rows[0].Imported)
}

func TestOntologyRowsSortsByID(t *testing.T) {
	pinned := []json.RawMessage{
		pinnedVocabularyRaw(t, "https://vocab.test/z", ""),
		pinnedVocabularyRaw(t, "https://vocab.test/a", ""),
	}
	rows, err := OntologyRows(pinned, nil)
	require.NoError(t, err)
	require.Equal(t, "https://vocab.test/a", rows[0].ID)
	require.Equal(t, "https://vocab.test/z", rows[1].ID)
}

func TestOntologyRowsRejectsInvalidJSON(t *testing.T) {
	_, err := OntologyRows([]json.RawMessage{json.RawMessage(`{invalid`)}, nil)
	require.Error(t, err)
}

func TestOntologyRowsIncludesAnImportThatIsNotYetPinned(t *testing.T) {
	rows, err := OntologyRows(nil, []string{"oci://ghcr.io/cgs-earth/example:latest"})
	require.NoError(t, err)
	require.Equal(t, []OntologyRow{
		{ID: "oci://ghcr.io/cgs-earth/example:latest", Imported: true},
	}, rows)
}

func TestOntologyRowsDeduplicatesAPinThatIsAlsoAnImport(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "salmodule://github.com/cgs-earth/example", "application/ld+json")}
	rows, err := OntologyRows(pinned, []string{"salmodule://github.com/cgs-earth/example"})
	require.NoError(t, err)
	require.Equal(t, []OntologyRow{
		{ID: "salmodule://github.com/cgs-earth/example", Version: "urn:sha256:salmodule://github.com/cgs-earth/example", Format: "application/ld+json", Imported: true},
	}, rows)
}

func TestOntologyRowsUnionsPinsAndImportsSortedTogether(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := OntologyRows(pinned, []string{"oci://ghcr.io/cgs-earth/example:latest"})
	require.NoError(t, err)
	require.Equal(t, []OntologyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:https://schema.org/", Format: "application/ld+json", Imported: false},
		{ID: "oci://ghcr.io/cgs-earth/example:latest", Imported: true},
	}, rows)
}

func TestOntologyTableRowsSpellsOutImported(t *testing.T) {
	rows := OntologyTableRows([]OntologyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:abc", Format: "application/ld+json", Imported: true},
		{ID: "oci://ghcr.io/cgs-earth/example:latest", Imported: false},
	})

	require.Equal(t, [][]string{
		{"https://schema.org/", "urn:sha256:abc", "application/ld+json", "yes"},
		{"oci://ghcr.io/cgs-earth/example:latest", "", "", "no"},
	}, rows)
}

func TestProjectOntologyRowsReportsNoRowsWhenTheConfigFileIsMissing(t *testing.T) {
	rows, err := ProjectOntologyRows(filepath.Join(t.TempDir(), "config.jsonld"), testBase)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestProjectOntologyRowsUnionsPinnedNodesAndTheOntologyImports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonld")
	raw, err := json.Marshal(map[string]any{
		"@context": pkg.SalConfigContext,
		"@graph": []map[string]any{
			{
				"@id":         ".",
				"@type":       "owl:Ontology",
				"dc:title":    "My Ontology",
				"owl:imports": []map[string]string{{"@id": "https://schema.org/"}},
			},
			{
				"@id":            "https://schema.org/",
				"@type":          "owl:Ontology",
				"owl:versionIRI": map[string]string{"@id": "urn:sha256:abc"},
				"dcterms:format": "application/ld+json",
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0644))

	rows, err := ProjectOntologyRows(path, testBase)
	require.NoError(t, err)
	require.Equal(t, []OntologyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:abc", Format: "application/ld+json", Imported: true},
	}, rows)
}
