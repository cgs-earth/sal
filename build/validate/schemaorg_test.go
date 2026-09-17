package validate

import (
	"context"
	"fmt"
	"testing"
)

// testSchemaOrgVocabulary stands in for the schema.org release document, so the
// tests that validate schema.org terms never reach schema.org itself.
const testSchemaOrgVocabulary = `@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> .
@prefix schema: <https://schema.org/> .

schema:Thing a rdfs:Class .
schema:Person a rdfs:Class .
schema:Place a rdfs:Class .
schema:name a rdf:Property .
schema:jobTitle a rdf:Property .
schema:telephone a rdf:Property .
schema:url a rdf:Property .
schema:birthDate a rdf:Property .
schema:email a rdf:Property .
schema:worksFor a rdf:Property .
`

// testHyFeaturesVocabulary stands in for the HY_Features vocabulary, which is
// served from its namespace without the trailing slash.
const testHyFeaturesVocabulary = `@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix hyf: <https://www.opengis.net/def/schema/hy_features/hyf/> .

hyf:HY_HydrometricFeature a rdfs:Class .
hyf:HY_HydroLocation a rdfs:Class .
`

// schemaOrgValidator validates against stand-ins for the schema.org and
// HY_Features vocabularies, served from the URLs sal resolves their namespaces
// to, and fails on any other fetch.
func schemaOrgValidator(t *testing.T) *Validator {
	t.Helper()
	pins := EphemeralVocabularies()
	pins.Fetch = func(_ context.Context, u string) ([]byte, string, PinnedVersion, error) {
		switch u {
		case schemaOrgDocumentURL:
			return []byte(testSchemaOrgVocabulary), "text/turtle", PinnedVersion{}, nil
		case "https://www.opengis.net/def/schema/hy_features/hyf":
			return []byte(testHyFeaturesVocabulary), "text/turtle", PinnedVersion{}, nil
		}
		return nil, "", PinnedVersion{}, fmt.Errorf("%s should not have been dereferenced", u)
	}
	return NewValidator(pins, testBase, nil)
}
