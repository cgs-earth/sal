package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/require"
)

// moduleTestOntology is a valid module ontology using no vocabulary but the
// ones moduleTestVocabulary serves. Line 10 is its ontology node and 21 the
// property shapes of its stdoutShape.
const moduleTestOntology = `{
  "@context": {
    "owl": "http://www.w3.org/2002/07/owl#",
    "rdfs": "http://www.w3.org/2000/01/rdf-schema#",
    "sh": "http://www.w3.org/ns/shacl#",
    "xsd": "http://www.w3.org/2001/XMLSchema#",
    "salmodule": "https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#"
  },
  "@graph": [
    {"@id": ".", "@type": "owl:Ontology", "rdfs:label": "Weather stations"},
    {
      "@id": "StationFetcher", "@type": "owl:Class", "rdfs:subClassOf": {"@id": "salmodule:Task"},
      "salmodule:taskShape": {
        "@type": "sh:NodeShape",
        "sh:targetClass": {"@id": "StationFetcher"},
        "sh:property": [{"sh:path": {"@id": "region"}, "sh:minCount": 1, "sh:datatype": {"@id": "xsd:string"}}]
      },
      "salmodule:stdoutShape": {
        "@type": "sh:NodeShape",
        "sh:targetClass": {"@id": "Station"},
        "sh:property": [{"sh:path": {"@id": "name"}, "sh:minCount": 1, "sh:maxCount": 1}]
      }
    },
    {"@id": "Station", "@type": "owl:Class"},
    {"@id": "name", "@type": "owl:DatatypeProperty", "rdfs:domain": {"@id": "Station"}, "rdfs:range": {"@id": "xsd:string"}},
    {"@id": "region", "@type": "owl:DatatypeProperty", "rdfs:domain": {"@id": "StationFetcher"}, "rdfs:range": {"@id": "xsd:string"}}
  ]
}
`

// moduleTestVocabulary defines every term moduleTestOntology uses from the
// owl, rdfs, sh, and salmodule vocabularies, and is served for all of them.
const moduleTestVocabulary = `@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix sh: <http://www.w3.org/ns/shacl#> .
@prefix salmodule: <https://w3id.org/sal/cgs-earth/sal/ontology/salmodule#> .

owl:Ontology a owl:Class . owl:Class a owl:Class . owl:DatatypeProperty a owl:Class .
rdfs:label a owl:AnnotationProperty . rdfs:subClassOf a owl:ObjectProperty .
rdfs:domain a owl:ObjectProperty . rdfs:range a owl:ObjectProperty .
sh:NodeShape a owl:Class . sh:targetClass a owl:ObjectProperty . sh:property a owl:ObjectProperty .
sh:path a owl:ObjectProperty . sh:minCount a owl:DatatypeProperty . sh:maxCount a owl:DatatypeProperty .
sh:datatype a owl:ObjectProperty .
salmodule:Task a owl:Class . salmodule:taskShape a owl:AnnotationProperty . salmodule:stdoutShape a owl:AnnotationProperty .
`

// newModuleTestProject creates a SAL project that is also a SAL module, with
// ontology written as its ontology.jsonld, makes it the working directory,
// and serves every vocabulary the ontology declares without the network. The
// path it returns has its symlinks resolved, as the project root validate
// reports has: on macOS the temporary directory sits under /var, a symlink to
// /private/var.
func newModuleTestProject(t *testing.T, ontology string) string {
	t.Helper()
	project, err := filepath.EvalSymlinks(newPinsTestProject(t))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(project, "data.ttl")))
	require.NoError(t, os.WriteFile(filepath.Join(project, salmodule.OntologyFile), []byte(ontology), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(project, salmodule.DockerfileName), []byte("FROM scratch\n"), 0644))

	original := projectVocabularies
	projectVocabularies = func(refresh bool) (*validate.PinnedVocabularies, error) {
		pins, err := original(refresh)
		if err != nil {
			return nil, err
		}
		pins.Fetch = func(context.Context, string) ([]byte, string, validate.PinnedVersion, error) {
			return []byte(moduleTestVocabulary), "text/turtle", validate.PinnedVersion{}, nil
		}
		return pins, nil
	}
	t.Cleanup(func() { projectVocabularies = original })
	return project
}

func TestValidateChecksAModuleWhoseRootHoldsOntologyAndDockerfile(t *testing.T) {
	newModuleTestProject(t, moduleTestOntology)

	_, err := (&ValidateCmd{}).Run()

	require.NoError(t, err)
}

func TestValidateReportsAModuleProblemWithItsLine(t *testing.T) {
	project := newModuleTestProject(t, strings.Replace(moduleTestOntology, `"@id": "."`, `"@id": "stations"`, 1))

	_, err := (&ValidateCmd{}).Run()

	require.Error(t, err)
	require.Equal(t, filepath.Join(project, salmodule.OntologyFile)+`:10: The owl:Ontology node is named "@id": "."`, err.Error())
}

// The module checks come on top of validating the ontology as RDF, so a typo
// in a SHACL term and a structural problem are reported together.
func TestValidateReportsRDFAndModuleProblemsTogether(t *testing.T) {
	ontology := strings.Replace(moduleTestOntology, `"@id": "."`, `"@id": "stations"`, 1)
	ontology = strings.Replace(ontology, `"sh:maxCount": 1`, `"sh:maxCountt": 1`, 1)
	project := newModuleTestProject(t, ontology)

	_, err := (&ValidateCmd{}).Run()

	require.Error(t, err)
	var errs validate.MultiError
	require.ErrorAs(t, err, &errs)
	require.Len(t, errs, 2)
	require.ErrorContains(t, errs[0], "ontology.jsonld:21: undefined term sh:maxCountt")
	require.Equal(t, filepath.Join(project, salmodule.OntologyFile)+`:10: The owl:Ontology node is named "@id": "."`, errs[1].Error())
}

func TestValidateChecksAModuleOnlyWhenAskedOrWhenBothFilesExist(t *testing.T) {
	project := newModuleTestProject(t, strings.Replace(moduleTestOntology, `"@id": "."`, `"@id": "stations"`, 1))
	require.NoError(t, os.Remove(filepath.Join(project, salmodule.DockerfileName)))

	// without a Dockerfile the project is not a module, and the ontology is
	// only validated as RDF
	_, err := (&ValidateCmd{}).Run()
	require.NoError(t, err)

	// asked to, validate checks it anyway and reports what is missing
	_, err = (&ValidateCmd{SalModule: true}).Run()
	require.Error(t, err)
	require.ErrorContains(t, err, filepath.Join(project, salmodule.DockerfileName)+": a SAL module needs a Dockerfile")
	require.ErrorContains(t, err, `ontology.jsonld:10: The owl:Ontology node is named "@id": "."`)
}

func TestValidateChecksTheOneDirectoryNamedOnTheCommandLine(t *testing.T) {
	project := newModuleTestProject(t, moduleTestOntology)
	module := filepath.Join(project, "module")
	require.NoError(t, os.MkdirAll(module, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(module, salmodule.OntologyFile), []byte(strings.Replace(moduleTestOntology, `"@id": "."`, `"@id": "stations"`, 1)), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(module, salmodule.DockerfileName), []byte("FROM scratch\n"), 0644))

	_, err := (&ValidateCmd{Paths: []string{"module"}}).Run()

	require.Error(t, err)
	require.Equal(t, filepath.Join("module", salmodule.OntologyFile)+`:10: The owl:Ontology node is named "@id": "."`, err.Error())
}
