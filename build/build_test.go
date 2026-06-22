package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cgs-earth/json-gold/ld"
)

const testSchemaOrgBase = "http://schema.org/"

func TestRunReportsUndefinedSchemaOrgTermWithLineNumber(t *testing.T) {
	path := filepath.Join("testdata", "incorrect", "name.jsonld")

	var stdout, stderr bytes.Buffer
	code := run([]string{path}, &stdout, &stderr, schemaOrgTestLoader{}, schemaOrgVocabularyFetch)

	if code != 1 {
		t.Fatalf("Run() code = %d, want 1", code)
	}
	got := stderr.String()
	if !strings.Contains(got, path+":4:") {
		t.Fatalf("Run() stderr = %q, want line 4", got)
	}
	if !strings.Contains(got, "undefined term schema:namee") {
		t.Fatalf("Run() stderr = %q, want undefined schema:namee", got)
	}
}

func TestRunValidatesSchemaOrgJSONLD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "person.jsonld")
	writeTestFile(t, path, `{
  "@context": "http://schema.org/",
  "@type": "Person",
  "name": "Jane Doe",
  "jobTitle": "Professor",
  "telephone": "(425) 123-4567",
  "url": "http://www.janedoe.com"
}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{path}, &stdout, &stderr, schemaOrgTestLoader{}, schemaOrgVocabularyFetch)

	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Validated 1 RDF file(s).") {
		t.Fatalf("Run() stdout = %q", stdout.String())
	}
}

func TestRunReportsUndefinedTermFromArbitraryVocabulary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.jsonld")
	writeTestFile(t, path, `{
  "@context": "https://example.com/context",
  "ex:Missing": "value"
}`)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{path},
		&stdout,
		&stderr,
		exampleVocabularyLoader{},
		exampleVocabularyFetch,
	)

	if code != 1 {
		t.Fatalf("Run() code = %d, want 1", code)
	}
	got := stderr.String()
	if !strings.Contains(got, path+":3:") {
		t.Fatalf("Run() stderr = %q, want line 3", got)
	}
	if !strings.Contains(got, "undefined term ex:Missing") {
		t.Fatalf("Run() stderr = %q, want undefined ex:Missing", got)
	}
}

func TestRunValidatesArbitraryVocabularyTerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.jsonld")
	writeTestFile(t, path, `{
  "@context": "https://example.com/context",
  "ex:Known": "value"
}`)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{path},
		&stdout,
		&stderr,
		exampleVocabularyLoader{},
		exampleVocabularyFetch,
	)

	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
}

func TestRunReportsUndefinedTurtleTermWithLineNumber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.ttl")
	writeTestFile(t, path, `@prefix ex: <https://example.com/vocab#> .

ex:Known ex:Known ex:Known ,
                   ex:Missing .
`)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{path},
		&stdout,
		&stderr,
		exampleVocabularyLoader{},
		exampleVocabularyFetch,
	)

	if code != 1 {
		t.Fatalf("Run() code = %d, want 1", code)
	}
	got := stderr.String()
	if !strings.Contains(got, path+":4:") {
		t.Fatalf("Run() stderr = %q, want line 4", got)
	}
	if !strings.Contains(got, "undefined term ex:Missing") {
		t.Fatalf("Run() stderr = %q, want undefined ex:Missing", got)
	}
}

func TestRunValidatesTurtleTerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.ttl")
	writeTestFile(t, path, `@prefix ex: <https://example.com/vocab#> .

ex:Known ex:Known ex:Known .
`)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{path},
		&stdout,
		&stderr,
		exampleVocabularyLoader{},
		exampleVocabularyFetch,
	)

	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Validated 1 RDF file(s).") {
		t.Fatalf("Run() stdout = %q", stdout.String())
	}
}

func TestRunExpandsInputDirectories(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "person.jsonld"), `{
  "@context": "http://schema.org/",
  "@type": "Person",
  "name": "Jane Doe"
}`)
	writeTestFile(t, filepath.Join(dir, "skip.ttl"), "@prefix ex: <https://example.com/> .\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{dir}, &stdout, &stderr, schemaOrgTestLoader{}, schemaOrgVocabularyFetch)

	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Validated 2 RDF file(s).") {
		t.Fatalf("Run() stdout = %q", stdout.String())
	}
}

func TestSeedCoreVocabularyCache(t *testing.T) {
	cache := vocabularyCache{
		cacheDir: defaultVocabularyCacheDir(),
		cache:    map[string]vocabulary{},
		fetch: func(u string) ([]byte, string, error) {
			switch u {
			case "http://schema.org/", "https://schema.org/":
				return []byte(schemaOrgTurtleVocabulary(u)), "text/turtle", nil
			case "https://www.opengis.net/def/schema/hy_features/hyf/":
				return []byte(`@prefix hyf: <https://www.opengis.net/def/schema/hy_features/hyf/> .
hyf:HY_HydrometricFeature a hyf:Class .
hyf:HY_HydroLocation a hyf:Class .
hyf:HY_IndirectPosition a hyf:Class .
hyf:HydroLocationType a hyf:Property .
hyf:containingCatchment a hyf:Property .
hyf:linearElement a hyf:Property .
hyf:referencedPosition a hyf:Property .
`), "text/turtle", nil
			case "http://www.opengis.net/ont/geosparql":
				return []byte(`@prefix gsp: <http://www.opengis.net/ont/geosparql#> .
gsp:hasGeometry a gsp:Property .
gsp:asWKT a gsp:Property .
gsp:crs a gsp:Property .
gsp:sfWithin a gsp:Property .
gsp:wktLiteral a gsp:Class .
`), "text/turtle", nil
			case "http://purl.org/dc/terms/":
				return []byte(`@prefix dc: <http://purl.org/dc/terms/> .
dc:conformsTo a dc:Property .
`), "text/turtle", nil
			default:
				return nil, "", fmt.Errorf("unexpected url %s", u)
			}
		},
	}

	for _, base := range []string{
		"http://schema.org/",
		"https://schema.org/",
		"https://www.opengis.net/def/schema/hy_features/hyf/",
		"http://www.opengis.net/ont/geosparql#",
		"http://purl.org/dc/terms/",
	} {
		if _, err := cache.load(base); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCachedDocumentLoaderPersistsBetweenCalls(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int64
	cache := vocabularyCache{
		cacheDir: dir,
		cache:    map[string]vocabulary{},
		fetch: func(u string) ([]byte, string, error) {
			calls.Add(1)
			if u != "https://example.com/vocab" {
				return nil, "", fmt.Errorf("unexpected url %s", u)
			}
			return []byte(`@prefix ex: <https://example.com/vocab#> .
ex:Thing a ex:Thing .`), "text/turtle", nil
		},
	}

	first, err := cache.load("https://example.com/vocab")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.load("https://example.com/vocab")
	if err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls.Load())
	}
	if !first.terms["https://example.com/vocab#Thing"] || !second.terms["https://example.com/vocab#Thing"] {
		t.Fatalf("cache did not retain expected term: %#v %#v", first.terms, second.terms)
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

type schemaOrgTestLoader struct{}

func (schemaOrgTestLoader) LoadDocument(u string) (*ld.RemoteDocument, error) {
	if u == testSchemaOrgBase || u == "https://schema.org/" {
		return &ld.RemoteDocument{
			DocumentURL: testSchemaOrgBase,
			Document: map[string]any{
				"@context": map[string]any{
					"@vocab": testSchemaOrgBase,
					"schema": testSchemaOrgBase,
				},
				"@graph": []any{
					map[string]any{"@id": "schema:Person"},
					map[string]any{"@id": "schema:jobTitle"},
					map[string]any{"@id": "schema:name"},
					map[string]any{"@id": "schema:telephone"},
					map[string]any{"@id": "schema:url"},
				},
			},
		}, nil
	}
	return ld.NewDefaultDocumentLoader(nil).LoadDocument(u)
}

func schemaOrgVocabularyFetch(u string) ([]byte, string, error) {
	if u != testSchemaOrgBase && u != "https://schema.org/" {
		return nil, "", fmt.Errorf("unexpected url %s", u)
	}
	return []byte(schemaOrgTurtleVocabulary(u)), "text/turtle", nil
}

func schemaOrgTurtleVocabulary(base string) string {
	_ = base
	terms := []string{
		"Person",
		"Place",
		"PropertyValue",
		"GovernmentOrganization",
		"GeoCoordinates",
		"Dataset",
		"DataDownload",
		"identifier",
		"name",
		"provider",
		"geo",
		"subjectOf",
		"description",
		"variableMeasured",
		"temporalCoverage",
		"distribution",
		"contentUrl",
		"encodingFormat",
		"unitText",
		"measurementTechnique",
		"measurementMethod",
		"publisher",
		"jobTitle",
		"telephone",
		"url",
		"propertyID",
		"value",
		"latitude",
		"longitude",
	}
	var b strings.Builder
	b.WriteString("@prefix schema: <https://schema.org/> .\n")
	b.WriteString("@prefix schemah: <http://schema.org/> .\n\n")
	for _, term := range terms {
		fmt.Fprintf(&b, "schema:%s a schema:Class .\n", term)
		fmt.Fprintf(&b, "schemah:%s a schemah:Class .\n", term)
	}
	return b.String()
}

type exampleVocabularyLoader struct{}

func (exampleVocabularyLoader) LoadDocument(u string) (*ld.RemoteDocument, error) {
	if u == "https://example.com/context" {
		return &ld.RemoteDocument{
			DocumentURL: u,
			Document: map[string]any{
				"@context": map[string]any{
					"ex": "https://example.com/vocab#",
				},
			},
		}, nil
	}
	return ld.NewDefaultDocumentLoader(nil).LoadDocument(u)
}

func exampleVocabularyFetch(u string) ([]byte, string, error) {
	if u != "https://example.com/vocab" {
		return nil, "", fmt.Errorf("unexpected url %s", u)
	}
	return []byte(`@prefix ex: <https://example.com/vocab#> .
ex:Known a ex:Class .
`), "text/turtle", nil
}
