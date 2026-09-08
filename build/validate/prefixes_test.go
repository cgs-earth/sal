package validate

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// ephemeralValidator validates without ever dereferencing a vocabulary, since
// the prefix checks only look at what the files declared.
func ephemeralValidator(t *testing.T, replacements map[string]string) *Validator {
	t.Helper()
	pins := EphemeralVocabularies()
	pins.Fetch = func(u string) ([]byte, string, PinnedVersion, error) {
		return nil, "", PinnedVersion{}, fmt.Errorf("%s should not have been dereferenced", u)
	}
	return NewValidator(pins, testBase, replacements)
}

func TestPrefixesWithoutTerminatorReportsANamespaceEndingInNeitherSlashNorHash(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "bare.ttl", `
		@prefix foo: <https://foo.com> .
		@prefix slash: <https://vocab.test/things/> .
		@prefix hash: <https://vocab.test/things#> .

		<widgets/1> a <Widget> .
	`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	suspicious := validator.PrefixesWithoutTerminator()

	require.Equal(t, []DeclaredPrefix{{Namespace: "https://foo.com", Paths: []string{path}}}, suspicious)
}

// A JSON-LD context entry without a terminator is never usable as a prefix,
// since a compact IRI written against it is already rejected as an undefined
// prefix, so @vocab is the one JSON-LD declaration the check can see.
func TestPrefixesWithoutTerminatorReportsAJSONLDVocab(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "vocab.jsonld", `{
		"@context": {"@vocab": "https://foo.com"},
		"@id": "`+testBase+`thing",
		"@type": "`+testBase+`Thing"
	}`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	suspicious := validator.PrefixesWithoutTerminator()

	require.Equal(t, []DeclaredPrefix{{Namespace: "https://foo.com", Paths: []string{path}}}, suspicious)
}

// A prefix map is what a project uses to fix a namespace it cannot edit, so a
// namespace mapped onto a well formed one is no longer suspicious.
func TestPrefixesWithoutTerminatorSeesThePrefixMapsApplied(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "mapped.ttl", `
		@prefix foo: <https://foo.com> .

		<widgets/1> a <Widget> .
	`)
	validator := ephemeralValidator(t, map[string]string{"https://foo.com": "https://foo.com/"})
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	require.Empty(t, validator.PrefixesWithoutTerminator())
}

func TestPrefixesWithoutTerminatorListsEveryFileDeclaringTheNamespace(t *testing.T) {
	first := writeTurtleTestFileNamed(t, "a.ttl", `
		@prefix foo: <https://foo.com> .
		<widgets/1> a <Widget> .
	`)
	second := writeTurtleTestFileNamed(t, "b.ttl", `
		@prefix foo: <https://foo.com> .
		<widgets/2> a <Widget> .
	`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(first)
	require.NoError(t, err)
	_, err = validator.ValidateFile(second)
	require.NoError(t, err)

	suspicious := validator.PrefixesWithoutTerminator()

	require.Len(t, suspicious, 1)
	require.ElementsMatch(t, []string{first, second}, suspicious[0].Paths)
}

func TestConflictingPrefixesRejectsHttpBesideHttps(t *testing.T) {
	first := writeTurtleTestFileNamed(t, "http.ttl", `
		@prefix schema: <http://schema.org/> .
		<widgets/1> a <Widget> .
	`)
	second := writeTurtleTestFileNamed(t, "https.ttl", `
		@prefix schema: <https://schema.org/> .
		<widgets/2> a <Widget> .
	`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(first)
	require.NoError(t, err)
	_, err = validator.ValidateFile(second)
	require.NoError(t, err)

	conflicts := validator.ConflictingPrefixes()

	require.Equal(t, []PrefixConflict{{
		Vocabulary:   "schema.org",
		MixesSchemes: true,
		Spellings: []DeclaredPrefix{
			{Namespace: "http://schema.org/", Paths: []string{first}},
			{Namespace: "https://schema.org/", Paths: []string{second}},
		},
	}}, conflicts)
}

func TestConflictingPrefixesRejectsANamespaceWithAndWithoutItsTrailingSlash(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "mixed.ttl", `
		@prefix schema: <https://schema.org/> .
		@prefix sdo: <https://schema.org> .
		<widgets/1> a <Widget> .
	`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	conflicts := validator.ConflictingPrefixes()

	require.Equal(t, []PrefixConflict{{
		Vocabulary:       "schema.org",
		MixesTerminators: true,
		Spellings: []DeclaredPrefix{
			{Namespace: "https://schema.org", Paths: []string{path}},
			{Namespace: "https://schema.org/", Paths: []string{path}},
		},
	}}, conflicts)
}

func TestConflictingPrefixesRejectsAHashBesideASlash(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "mixed.ttl", `
		@prefix hash: <https://vocab.test/things#> .
		@prefix slash: <https://vocab.test/things/> .
		<widgets/1> a <Widget> .
	`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	require.Len(t, validator.ConflictingPrefixes(), 1)
}

func TestConflictingPrefixesDescribesAMixOfSchemeAndTerminatorAsBoth(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "mixed.ttl", `
		@prefix a: <http://schema.org> .
		@prefix b: <http://schema.org/> .
		@prefix c: <https://schema.org/> .
		<widgets/1> a <Widget> .
	`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	conflicts := validator.ConflictingPrefixes()

	require.Len(t, conflicts, 1)
	require.True(t, conflicts[0].MixesSchemes)
	require.True(t, conflicts[0].MixesTerminators)
}

func TestConflictingPrefixesAcceptsTheSameNamespaceDeclaredEverywhere(t *testing.T) {
	first := writeTurtleTestFileNamed(t, "a.ttl", `
		@prefix schema: <https://schema.org/> .
		<widgets/1> a <Widget> .
	`)
	second := writeTurtleTestFileNamed(t, "b.jsonld", `{
		"@context": {"sdo": "https://schema.org/"},
		"@id": "https://example.test/thing",
		"@type": "sdo:Thing"
	}`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(first)
	require.NoError(t, err)
	_, err = validator.ValidateFile(second)
	require.NoError(t, err)

	require.Empty(t, validator.ConflictingPrefixes())
}

// Two spellings a prefix map folds onto one namespace are not a conflict, since
// that is exactly how a project reconciles a file it cannot edit.
func TestConflictingPrefixesAcceptsSpellingsAPrefixMapFoldsTogether(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "mixed.ttl", `
		@prefix schema: <http://schema.org/> .
		@prefix sdo: <https://schema.org/> .
		<widgets/1> a <Widget> .
	`)
	validator := ephemeralValidator(t, map[string]string{"http://schema.org/": "https://schema.org/"})
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	require.Empty(t, validator.ConflictingPrefixes())
}

func TestConflictingPrefixesAcceptsDistinctVocabularies(t *testing.T) {
	path := writeTurtleTestFileNamed(t, "distinct.ttl", `
		@prefix hyf: <https://www.opengis.net/def/schema/hy_features/hyf/> .
		@prefix gsp: <http://www.opengis.net/ont/geosparql#> .
		<widgets/1> a <Widget> .
	`)
	validator := ephemeralValidator(t, nil)
	_, err := validator.ValidateFile(path)
	require.NoError(t, err)

	require.Empty(t, validator.ConflictingPrefixes())
}
