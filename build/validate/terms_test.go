package validate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const testTermsDocument = `@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix things: <https://vocab.test/things#> .

things:Widget a owl:Class ;
    rdfs:label "Widget" .
things:Gadget a rdfs:Class .
things:name a rdf:Property ;
    rdfs:domain things:Widget .
things:partOf a owl:ObjectProperty .
things:Blue a things:Colour .
<https://vocab.test/things#Colour> a rdfs:Class .
<https://vocab.test/other#Elsewhere> a owl:Class .
`

// pinnedTerms pins document under id in a temporary project, saves it, and
// reads the pins back from disk the way the server does, so Terms is
// exercised over the written document rather than the one still pending.
func pinnedTerms(t *testing.T, id string, document string, mediaType string) VocabularyTerms {
	t.Helper()
	projectDir := t.TempDir()
	pins := newTestPins(t, projectDir, document, nil)
	pins.Fetch = func(context.Context, string) ([]byte, string, PinnedVersion, error) {
		return []byte(document), mediaType, PinnedVersion{}, nil
	}
	_, _, _, err := pins.Document(context.Background(), id, id)
	require.NoError(t, err)
	require.NoError(t, pins.Save())

	reloaded, err := LoadPinnedVocabularies(filepath.Join(projectDir, "config.jsonld"), filepath.Join(projectDir, "data"))
	require.NoError(t, err)
	terms, err := reloaded.Terms(id)
	require.NoError(t, err)
	return terms
}

func TestTermsClassifiesWhatThePinnedDocumentDefinesUnderItsNamespace(t *testing.T) {
	terms := pinnedTerms(t, testVocabularyNamespace, testTermsDocument, "text/turtle")

	require.Equal(t, testVocabularyNamespace, terms.ID)
	require.Equal(t, testVocabularyNamespace, terms.Namespace)
	require.Equal(t, "things", terms.Prefix, "the prefix the document binds its own namespace to")
	require.Equal(t, "text/turtle", terms.Format)
	require.Contains(t, terms.Version, "urn:sha256:")
	require.Equal(t, []string{"Colour", "Gadget", "Widget"}, terms.Classes)
	require.Equal(t, []string{"name", "partOf"}, terms.Properties)
	require.Equal(t, []string{"Blue"}, terms.Others, "an instance is a term too, outside both kinds")
	require.Equal(t, []TermStatement{{Predicate: "rdfs:label", Value: "Widget"}}, terms.Statements["Widget"])
	require.NotContains(t, terms.Statements, "Gadget", "a term the document only types has no statements worth showing")
}

func TestTermsLeavesOutWhatAnotherNamespaceDefines(t *testing.T) {
	terms := pinnedTerms(t, testVocabularyNamespace, testTermsDocument, "text/turtle")
	for _, name := range append(append(terms.Classes, terms.Properties...), terms.Others...) {
		require.NotEqual(t, "Elsewhere", name)
		require.NotEqual(t, "Class", name)
	}
}

func TestTermsFallsBackToAConventionalPrefix(t *testing.T) {
	document := `<https://schema.org/Person> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://www.w3.org/2000/01/rdf-schema#Class> .
`
	terms := pinnedTerms(t, "https://schema.org/", document, "application/n-triples")
	require.Equal(t, "schema", terms.Prefix)
	require.Equal(t, []string{"Person"}, terms.Classes)
}

func TestTermsDerivesAPrefixFromTheNamespacePath(t *testing.T) {
	document := `<https://example.org/vocab/Hy_Features/Thing> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://www.w3.org/2002/07/owl#Class> .
`
	terms := pinnedTerms(t, "https://example.org/vocab/Hy_Features/", document, "application/n-triples")
	require.Equal(t, "hy_features", terms.Prefix)
}

func TestTermsReadsTheSeparatorOfAnOntologyImportedByABareIRI(t *testing.T) {
	document := `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/onto> a owl:Ontology .
<https://vocab.test/onto#A> a owl:Class .
<https://vocab.test/onto#B> a owl:Class .
`
	terms := pinnedTerms(t, "https://vocab.test/onto", document, "text/turtle")
	require.Equal(t, "https://vocab.test/onto#", terms.Namespace)
	require.Equal(t, "onto", terms.Prefix)
	require.Equal(t, []string{"A", "B"}, terms.Classes)
}

func TestTermsIsAnErrorForAVocabularyThatIsNotPinned(t *testing.T) {
	pins := EphemeralVocabularies()
	_, err := pins.Terms("https://vocab.test/nothing#")
	require.ErrorContains(t, err, "not a pinned vocabulary")
}

func TestTermsNeverFetchesAMissingDocument(t *testing.T) {
	projectDir := t.TempDir()
	fetches := 0
	pins := newTestPins(t, projectDir, testVocabularyDocument, &fetches)
	_, _, _, err := pins.Document(context.Background(), testVocabularyNamespace, testVocabularyNamespace)
	require.NoError(t, err)
	require.NoError(t, pins.Save())
	require.Equal(t, 1, fetches)

	// the pins know the document, but the blob store it was written to is gone
	reloaded, err := LoadPinnedVocabularies(filepath.Join(projectDir, "config.jsonld"), filepath.Join(projectDir, "elsewhere"))
	require.NoError(t, err)
	reloaded.Fetch = pins.Fetch
	_, err = reloaded.Terms(testVocabularyNamespace)
	require.Error(t, err)
	require.Equal(t, 1, fetches)
}

func TestTermsReadsWhatTheDocumentSaysAboutATerm(t *testing.T) {
	document := `@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix skos: <http://www.w3.org/2004/02/skos/core#> .
@prefix schema: <https://schema.org/> .
@prefix things: <https://vocab.test/things#> .
@prefix ex: <https://example.org/elsewhere/> .

things:Widget a owl:Class ;
    rdfs:subClassOf schema:Thing, ex:Part ;
    rdfs:comment "  A thing with parts.  " ;
    rdfs:label "Widget" ;
    rdfs:isDefinedBy things: ;
    skos:example "a gear" .
things:partOf a owl:ObjectProperty ;
    rdfs:domain things:Widget ;
    rdfs:range <https://vocab.test/other#Whole> ;
    owl:deprecated true .
things:Blue a things:Colour ;
    rdfs:label "Blue" .
things:Mystery a owl:Class .
<https://vocab.test/other#Elsewhere> a owl:Class ;
    rdfs:comment "Not this vocabulary's" .
`
	terms := pinnedTerms(t, testVocabularyNamespace, document, "text/turtle")
	require.Equal(t, []TermStatement{
		{Predicate: "rdfs:label", Value: "Widget"},
		{Predicate: "rdfs:comment", Value: "A thing with parts."},
		{Predicate: "skos:example", Value: "a gear"},
		{Predicate: "rdfs:subClassOf", Value: "ex:Part", IRI: true},
		{Predicate: "rdfs:subClassOf", Value: "schema:Thing", IRI: true},
	}, terms.Statements["Widget"], "descriptive statements in a fixed order, IRIs shortened with the document's and the conventional prefixes, rdf:type and isDefinedBy left out")
	require.Equal(t, []TermStatement{
		{Predicate: "rdfs:domain", Value: "things:Widget", IRI: true},
		{Predicate: "rdfs:range", Value: "https://vocab.test/other#Whole", IRI: true},
		{Predicate: "owl:deprecated", Value: "true"},
	}, terms.Statements["partOf"], "an IRI under a namespace nothing binds is written in full")
	require.Equal(t, []TermStatement{{Predicate: "rdfs:label", Value: "Blue"}}, terms.Statements["Blue"], "an instance has statements like any term")
	require.NotContains(t, terms.Statements, "Mystery", "a term the document says nothing about has no entry")
	require.NotContains(t, terms.Statements, "Elsewhere")
}

func TestTermsOrdersAndBoundsTheValuesOfOneStatement(t *testing.T) {
	document := `@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix things: <https://vocab.test/things#> .

things:Widget a owl:Class ;
    rdfs:comment "Ein Ding"@de, "A thing"@en-GB, "Une chose"@fr, "A thing, untagged" .
things:Gadget a owl:Class ;
    rdfs:subClassOf things:C1, things:C2, things:C3, things:C4, things:C5, things:C6, things:C7 .
`
	terms := pinnedTerms(t, testVocabularyNamespace, document, "text/turtle")
	require.Equal(t, []TermStatement{
		{Predicate: "rdfs:comment", Value: "A thing", Language: "en-gb"},
		{Predicate: "rdfs:comment", Value: "A thing, untagged"},
		{Predicate: "rdfs:comment", Value: "Ein Ding", Language: "de"},
		{Predicate: "rdfs:comment", Value: "Une chose", Language: "fr"},
	}, terms.Statements["Widget"], "English and untagged literals first, then the rest, each set sorted")
	require.Len(t, terms.Statements["Gadget"], statementValuesLimit)
	require.Equal(t, TermStatement{Predicate: "rdfs:subClassOf", Value: "things:C1", IRI: true}, terms.Statements["Gadget"][0])
}
