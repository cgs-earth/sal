package salmodule

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/turtle"
)

const testFileContent = "hello from the container\n"

func testFileDigest() string {
	sum := sha256.Sum256([]byte(testFileContent))
	return hex.EncodeToString(sum[:])
}

func runTestTask(t *testing.T, runner *fakeRunner, blobDir string) (TaskResult, error) {
	t.Helper()
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	return newTestResolver(runner).RunTask(context.Background(), ref, DefaultTaskInstanceEnvVar, "{}", blobDir)
}

func TestRunTaskCopiesFilesTheOutputNamesIntoTheBlobStore(t *testing.T) {
	blobDir := filepath.Join(t.TempDir(), "blobs")
	runner := &fakeRunner{
		ontology:       testOntology,
		runOutput:      `{"@id":"dataset","schema:hasPart":{"@id":"file:///tmp/test.txt"}}` + "\n",
		containerFiles: map[string]string{"/tmp/test.txt": testFileContent},
	}

	result, err := runTestTask(t, runner, blobDir)

	require.NoError(t, err)
	require.Equal(t, runner.runOutput, string(result.Output))
	require.Equal(t, []CopiedFile{{
		ContainerPath: "/tmp/test.txt",
		Name:          "test.txt",
		Modified:      testFileModified,
		Digest:        testFileDigest(),
		Path:          filepath.Join(blobDir, testFileDigest()),
	}}, result.Files)
	copied, err := os.ReadFile(filepath.Join(blobDir, testFileDigest()))
	require.NoError(t, err)
	require.Equal(t, testFileContent, string(copied))
}

// A file referenced more than once is copied a single time; the later
// references are warned about and skipped.
func TestRunTaskCopiesARepeatedFileOnlyOnce(t *testing.T) {
	runner := &fakeRunner{
		ontology: testOntology,
		runOutput: `{"@id":"a","schema:hasPart":{"@id":"file:///tmp/test.txt"}}` + "\n" +
			`{"@id":"b","schema:hasPart":{"@id":"file:///tmp/test.txt"}}` + "\n",
		containerFiles: map[string]string{"/tmp/test.txt": testFileContent},
	}

	result, err := runTestTask(t, runner, t.TempDir())

	require.NoError(t, err)
	require.Equal(t, []string{"/tmp/test.txt"}, runner.copies)
	require.Len(t, result.Files, 1)
}

func TestRunTaskCopiesEveryDistinctFile(t *testing.T) {
	runner := &fakeRunner{
		ontology:  testOntology,
		runOutput: `{"@id":"a","schema:hasPart":[{"@id":"file:///tmp/b.txt"},{"@id":"file:///tmp/a.txt"}]}` + "\n",
		containerFiles: map[string]string{
			"/tmp/a.txt": "a",
			"/tmp/b.txt": "b",
		},
	}

	result, err := runTestTask(t, runner, t.TempDir())

	require.NoError(t, err)
	require.Len(t, result.Files, 2)
	require.Equal(t, "a.txt", result.Files[0].Name)
	require.Equal(t, "b.txt", result.Files[1].Name)
	require.NotEqual(t, result.Files[0].Digest, result.Files[1].Digest)
}

func TestRunTaskFailsWhenANamedFileCannotBeCopied(t *testing.T) {
	blobDir := t.TempDir()
	runner := &fakeRunner{
		ontology:  testOntology,
		runOutput: `{"@id":"a","schema:hasPart":{"@id":"file:///tmp/missing.txt"}}` + "\n",
	}

	_, err := runTestTask(t, runner, blobDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "no such file /tmp/missing.txt")
	// a failed copy leaves nothing behind, not even its temporary file
	entries, err := os.ReadDir(blobDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// The output a task wrote before failing is still returned, since a task
// reports its own errors on stdout before exiting non-zero.
func TestRunTaskReturnsTheOutputOfAFailedContainer(t *testing.T) {
	runner := &fakeRunner{
		ontology:  testOntology,
		runOutput: `{"@type":"salmodule:Error","rdfs:comment":"boom"}` + "\n",
		runErr:    os.ErrClosed,
	}

	result, err := runTestTask(t, runner, t.TempDir())

	require.ErrorIs(t, err, os.ErrClosed)
	require.Equal(t, runner.runOutput, string(result.Output))
}

func TestContainerFilePathDecodesTheIRI(t *testing.T) {
	path, err := containerFilePath("file:///tmp/my%20file.txt")

	require.NoError(t, err)
	require.Equal(t, "/tmp/my file.txt", path)
}

func TestContainerFilePathRejectsAFileIRIWithAHost(t *testing.T) {
	_, err := containerFilePath("file://host/tmp/test.txt")

	require.Error(t, err)
	require.Contains(t, err.Error(), "file:///absolute/path")
}

func parseTestTurtle(t *testing.T, content string) *rdflibgo.Graph {
	t.Helper()
	graph := rdflibgo.NewGraph(rdflibgo.WithBase("https://example.test/project/"))
	require.NoError(t, turtle.Parse(graph, bytes.NewReader([]byte(content)), turtle.WithBase("https://example.test/project/")))
	return graph
}

func graphTriples(graph *rdflibgo.Graph) []string {
	var triples []string
	graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		object := triple.Object.String()
		if literal, ok := triple.Object.(rdflibgo.Literal); ok {
			object = `"` + literal.Lexical() + `"`
		}
		triples = append(triples, triple.Subject.String()+" "+triple.Predicate.Value()+" "+object)
		return true
	})
	return triples
}

func TestLinkCopiedFilesRewritesFileObjectsAndRecordsTheirNames(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file:///tmp/test.txt> ; schema:name "a dataset" .
	`)
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Name: "test.txt", Modified: testFileModified, Digest: testFileDigest(), Path: filepath.Join(t.TempDir(), testFileDigest())}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{file}))

	copyIRI := "urn:sha256:" + testFileDigest()
	require.ElementsMatch(t, []string{
		"https://example.test/project/dataset https://schema.org/hasPart " + copyIRI,
		`https://example.test/project/dataset https://schema.org/name "a dataset"`,
		copyIRI + ` http://www.w3.org/2000/01/rdf-schema#label "test.txt"`,
		copyIRI + ` http://purl.org/dc/terms/modified "2026-09-10T14:26:05Z"`,
		copyIRI + " http://www.w3.org/2002/07/owl#versionIRI " + copyIRI,
	}, graphTriples(graph))
}

// A file the task wrote as a literal rather than an IRI object was copied on
// sight, since coercion is only known once the graph exists; the copy is
// discarded rather than left in the blob store with nothing referring to it.
func TestLinkCopiedFilesDiscardsACopyNothingRefersToAsAnObject(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:url "file:///tmp/test.txt" .
	`)
	path := filepath.Join(t.TempDir(), testFileDigest())
	require.NoError(t, os.WriteFile(path, []byte(testFileContent), 0644))
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Name: "test.txt", Digest: testFileDigest(), Path: path}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{file}))

	require.NoFileExists(t, path)
	require.Equal(t, []string{`https://example.test/project/dataset https://schema.org/url "file:///tmp/test.txt"`}, graphTriples(graph))
}

func TestLinkCopiedFilesRejectsAFileObjectThatWasNotCopied(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file://host/tmp/test.txt> .
	`)

	err := LinkCopiedFiles(graph, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "file://host/tmp/test.txt")
}
