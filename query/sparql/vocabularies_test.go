package sparql

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/stretchr/testify/require"
)

func TestVocabulariesListsOnlyTheBuiltinsForARemoteTable(t *testing.T) {
	listing, err := DuckDBRunner{TablePath: "gs://bucket/sal/triples"}.Vocabularies(context.Background())
	require.NoError(t, err)
	require.NotNil(t, listing.Vocabularies, "an empty listing is an empty array, not null, for the UI")
	require.Len(t, listing.Vocabularies, 3)
	for _, vocabulary := range listing.Vocabularies {
		require.True(t, vocabulary.Builtin, vocabulary.Namespace)
		require.Empty(t, vocabulary.Version)
	}
}

func TestBuiltinVocabulariesCompleteTheEngineTerms(t *testing.T) {
	listing := withBuiltinVocabularies(VocabularyListing{Vocabularies: []Vocabulary{}})
	prefixes := []string{}
	for _, vocabulary := range listing.Vocabularies {
		prefixes = append(prefixes, vocabulary.Prefix)
	}
	require.Equal(t, []string{"geof", "uom", "xsd"}, prefixes)

	geof := listing.Vocabularies[0]
	require.Equal(t, geofNamespace, geof.Namespace)
	require.Contains(t, geof.Others, "sfIntersects")
	require.Contains(t, geof.Others, "distance")
	require.True(t, slices.IsSorted(geof.Others))
	require.Contains(t, geof.Statements["distance"][0].Value, "uom:metre")

	uom := listing.Vocabularies[1]
	require.Equal(t, uomNamespace, uom.Namespace)
	require.Equal(t, []string{"degree", "metre"}, uom.Others)

	xsd := listing.Vocabularies[2]
	require.Equal(t, validate.XSDNamespace, xsd.Namespace)
	require.Contains(t, xsd.Classes, "dateTime")
	require.Contains(t, xsd.Classes, "integer")
}

func TestGeofFunctionsAreTheTranslatedOnes(t *testing.T) {
	// Every documented function is one geoFunctionSQL translates, and every
	// translated one is documented.
	translated := map[string]bool{"distance": true}
	for name := range geofRelations {
		translated[name] = true
	}
	for name := range geofConstructors {
		translated[name] = true
	}
	documented := map[string]bool{}
	for _, function := range geofFunctions {
		documented[strings.ToLower(function.name)] = true
		require.True(t, translated[strings.ToLower(function.name)], "geof:%s is documented but not translated", function.name)
	}
	for name := range translated {
		require.True(t, documented[name], "geof:%s is translated but not documented", name)
	}
}

func TestBuiltinVocabulariesYieldToAPinnedOne(t *testing.T) {
	pinned := Vocabulary{ID: validate.XSDNamespace, Prefix: "xsd", Namespace: validate.XSDNamespace, Version: "urn:sha256:aaaa", Classes: []string{"string"}}
	listing := withBuiltinVocabularies(VocabularyListing{Vocabularies: []Vocabulary{pinned}})
	require.Len(t, listing.Vocabularies, 3)
	require.Equal(t, pinned, listing.Vocabularies[0], "the pinned document is kept over the built-in list")
	require.False(t, listing.Vocabularies[0].Builtin)
	require.True(t, listing.Vocabularies[1].Builtin)
}
