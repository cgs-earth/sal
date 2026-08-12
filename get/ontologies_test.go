package get

import (
	"encoding/json"
	"testing"

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
	rows, err := ontologyRows(pinned, []string{"https://schema.org/"})
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"https://schema.org/", "urn:sha256:https://schema.org/", "application/ld+json", "yes"},
	}, rows)
}

func TestOntologyRowsMarksAPinThatIsNotImported(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := ontologyRows(pinned, nil)
	require.NoError(t, err)
	require.Equal(t, "no", rows[0][3])
}

func TestOntologyRowsSortsByID(t *testing.T) {
	pinned := []json.RawMessage{
		pinnedVocabularyRaw(t, "https://vocab.test/z", ""),
		pinnedVocabularyRaw(t, "https://vocab.test/a", ""),
	}
	rows, err := ontologyRows(pinned, nil)
	require.NoError(t, err)
	require.Equal(t, "https://vocab.test/a", rows[0][0])
	require.Equal(t, "https://vocab.test/z", rows[1][0])
}

func TestOntologyRowsRejectsInvalidJSON(t *testing.T) {
	_, err := ontologyRows([]json.RawMessage{json.RawMessage(`{invalid`)}, nil)
	require.Error(t, err)
}

func TestOntologyRowsIncludesAnImportThatIsNotYetPinned(t *testing.T) {
	rows, err := ontologyRows(nil, []string{"oci://ghcr.io/cgs-earth/example:latest"})
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"oci://ghcr.io/cgs-earth/example:latest", "", "", "yes"},
	}, rows)
}

func TestOntologyRowsDeduplicatesAPinThatIsAlsoAnImport(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "salmodule://github.com/cgs-earth/example", "application/ld+json")}
	rows, err := ontologyRows(pinned, []string{"salmodule://github.com/cgs-earth/example"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, [][]string{
		{"salmodule://github.com/cgs-earth/example", "urn:sha256:salmodule://github.com/cgs-earth/example", "application/ld+json", "yes"},
	}, rows)
}

func TestOntologyRowsUnionsPinsAndImportsSortedTogether(t *testing.T) {
	pinned := []json.RawMessage{pinnedVocabularyRaw(t, "https://schema.org/", "application/ld+json")}
	rows, err := ontologyRows(pinned, []string{"oci://ghcr.io/cgs-earth/example:latest"})
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"https://schema.org/", "urn:sha256:https://schema.org/", "application/ld+json", "no"},
		{"oci://ghcr.io/cgs-earth/example:latest", "", "", "yes"},
	}, rows)
}
