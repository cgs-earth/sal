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

func TestVocabularyRowsMarksAPinThatIsAlsoImported(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := VocabularyRows(pinned, []string{"https://schema.org/"})
	require.NoError(t, err)
	require.Equal(t, []VocabularyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:https://schema.org/", Format: "application/ld+json", Imported: true},
	}, rows)
}

func TestVocabularyRowsMarksAPinThatIsNotImported(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := VocabularyRows(pinned, nil)
	require.NoError(t, err)
	require.False(t, rows[0].Imported)
}

func TestVocabularyRowsSortsByID(t *testing.T) {
	pinned := []json.RawMessage{
		pinnedVocabularyRaw(t, "https://vocab.test/z", ""),
		pinnedVocabularyRaw(t, "https://vocab.test/a", ""),
	}
	rows, err := VocabularyRows(pinned, nil)
	require.NoError(t, err)
	require.Equal(t, "https://vocab.test/a", rows[0].ID)
	require.Equal(t, "https://vocab.test/z", rows[1].ID)
}

func TestVocabularyRowsRejectsInvalidJSON(t *testing.T) {
	_, err := VocabularyRows([]json.RawMessage{json.RawMessage(`{invalid`)}, nil)
	require.Error(t, err)
}

func TestVocabularyRowsIncludesAnImportThatIsNotYetPinned(t *testing.T) {
	rows, err := VocabularyRows(nil, []string{"oci://ghcr.io/cgs-earth/example:latest"})
	require.NoError(t, err)
	require.Equal(t, []VocabularyRow{
		{ID: "oci://ghcr.io/cgs-earth/example:latest", Imported: true},
	}, rows)
}

func TestVocabularyRowsDeduplicatesAPinThatIsAlsoAnImport(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "salmodule://github.com/cgs-earth/example", "application/ld+json")}
	rows, err := VocabularyRows(pinned, []string{"salmodule://github.com/cgs-earth/example"})
	require.NoError(t, err)
	require.Equal(t, []VocabularyRow{
		{ID: "salmodule://github.com/cgs-earth/example", Version: "urn:sha256:salmodule://github.com/cgs-earth/example", Format: "application/ld+json", Imported: true},
	}, rows)
}

func TestVocabularyRowsUnionsPinsAndImportsSortedTogether(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := VocabularyRows(pinned, []string{"oci://ghcr.io/cgs-earth/example:latest"})
	require.NoError(t, err)
	require.Equal(t, []VocabularyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:https://schema.org/", Format: "application/ld+json", Imported: false},
		{ID: "oci://ghcr.io/cgs-earth/example:latest", Imported: true},
	}, rows)
}

func TestVocabularyTableRowsSpellsOutImported(t *testing.T) {
	rows := VocabularyTableRows([]VocabularyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:abc", Format: "application/ld+json", Imported: true},
		{ID: "oci://ghcr.io/cgs-earth/example:latest", Imported: false},
	})

	require.Equal(t, [][]string{
		{"https://schema.org/", "urn:sha256:abc", "application/ld+json", "yes"},
		{"oci://ghcr.io/cgs-earth/example:latest", "", "", "no"},
	}, rows)
}

func TestProjectVocabularyRowsReportsNoRowsWhenTheConfigFileIsMissing(t *testing.T) {
	rows, err := ProjectVocabularyRows(filepath.Join(t.TempDir(), "config.jsonld"), testBase)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestProjectVocabularyRowsUnionsPinnedNodesAndTheOntologyImports(t *testing.T) {
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

	rows, err := ProjectVocabularyRows(path, testBase)
	require.NoError(t, err)
	require.Equal(t, []VocabularyRow{
		{ID: "https://schema.org/", Version: "urn:sha256:abc", Format: "application/ld+json", Imported: true},
	}, rows)
}
