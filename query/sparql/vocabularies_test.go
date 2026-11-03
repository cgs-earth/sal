package sparql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVocabulariesListsNothingForARemoteTable(t *testing.T) {
	listing, err := DuckDBRunner{TablePath: "gs://bucket/sal/triples"}.Vocabularies(context.Background())
	require.NoError(t, err)
	require.NotNil(t, listing.Vocabularies, "an empty listing is an empty array, not null, for the UI")
	require.Empty(t, listing.Vocabularies)
}
