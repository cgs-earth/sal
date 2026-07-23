package validate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const testBase = "https://example.test/base/"

func writeJSONLDTestFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "data.jsonld")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestValidateJSONLDFileRejectsUndefinedSchemaOrgProperty(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": "http://schema.org/",
		"@type": "Person",
		"@id": "Bob",
		"namee": "Jane Doe",
		"jobTitle": "Professor",
		"telephone": "(425) 123-4567",
		"url": "http://www.janedoe.com"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "namee")
	require.Contains(t, err.Error(), path+":5:", "The failing term should be on line number 5")
}

func TestValidateJSONLDFileAcceptsInlineVocabDefinedSchemaTerms(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"@vocab": "https://schema.org/"
		},
		"@type": "Person",
		"@id": "Bob",
		"name": "Jane Doe",
		"jobTitle": "Professor"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.NoError(t, err)
}

func TestValidateJSONLDFileRejectsUndefinedInlineVocabType(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"@vocab": "https://schema.org/"
		},
		"@type": "Persson",
		"@id": "Bob",
		"name": "Jane Doe"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "Persson")
}

func TestValidateJSONLDFileRejectsUndefinedCompactProperty(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"schema": "https://schema.org/"
		},
		"@id": "Bob",
		"@type": "schema:Person",
		"schema:namee": "Jane Doe"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "schema:namee")
	require.Contains(t, err.Error(), path+":7:")
}

func TestValidateJSONLDFileRejectsUndeclaredCompactPropertyPrefix(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"schema": "https://schema.org/"
		},
		"@id": "Bob",
		"@type": "schema:Person",
		"sh:property": "name"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "sh:property")
	require.Contains(t, err.Error(), "prefix sh is not defined")
	require.Contains(t, err.Error(), path+":7:")
}

func TestValidateJSONLDFileRejectsUndeclaredCompactTypePrefix(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"schema": "https://schema.org/"
		},
		"@id": "Bob",
		"@type": "sh:NodeShape"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "sh:NodeShape")
	require.Contains(t, err.Error(), "prefix sh is not defined")
	require.Contains(t, err.Error(), path+":6:")
}

func TestValidateJSONLDFileRejectsUndeclaredCompactIDPrefix(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"schema": "https://schema.org/",
			"sh": "http://www.w3.org/ns/shacl#"
		},
		"@id": "Bob",
		"@type": "sh:NodeShape",
		"sh:datatype": {
			"@id": "xsd:string"
		}
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "xsd:string")
	require.Contains(t, err.Error(), "prefix xsd is not defined")
	require.Contains(t, err.Error(), path+":9:")
}

func TestValidateJSONLDFileReportsUndefinedTypeLine(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"@vocab": "https://schema.org/"
		},
		"@type": "Persson",
		"@id": "Bob",
		"name": "Jane Doe"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "Persson")
	require.Contains(t, err.Error(), path+":5:")
}

func TestValidateJSONLDFileReportsUndefinedArrayTypeValueLineOnce(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"@vocab": "https://schema.org/",
			"schema": "https://schema.org/",
			"hyf": "https://www.opengis.net/def/schema/hy_features/hyf/"
		},
		"@id": "https://geoconnex.us/iow/wqp/NALMS-F871468",
		"@type": [
			"hyf:HY_HydrometricFeature",
			"hyf:HY_HydroLocation",
			"PlaceEE"
		]
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined term")
	require.Contains(t, err.Error(), "schema:PlaceEE")
	require.Contains(t, err.Error(), path+":11:")
	require.NotContains(t, err.Error(), path+":8:")
}

func TestValidateJSONLDFileSkipsRelativeIDUnderBase(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": {
			"@vocab": "https://schema.org/"
		},
		"@id": "relative-person",
		"@type": "Person",
		"name": "Jane Doe"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.NoError(t, err)
}

func TestValidateJSONLDFileRejectsLocalIDWithoutType(t *testing.T) {
	path := writeJSONLDTestFile(t, `{
		"@context": "http://schema.org/",
		"@id": "Jane",
		"name": "Jane Doe",
		"jobTitle": "Professor",
		"telephone": "(425) 123-4567",
		"url": "http://www.janedoe.com"
	}`)

	_, err := ValidateRDFFile(path, nil, testBase)

	require.Error(t, err)
	var missingTypeErr missingTypeError
	require.ErrorAs(t, err, &missingTypeErr)
	require.Equal(t, testBase+"Jane", missingTypeErr.IRI)
}

func TestValidateSALModuleOntology(t *testing.T) {
	_, err := ValidateRDFFile("../../salmodule/sal_ontology.jsonld", nil, testBase)

	require.NoError(t, err)
}
