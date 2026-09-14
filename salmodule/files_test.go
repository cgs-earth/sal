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
		BlobPath:      testFileDigest(),
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
	path, isDir, err := containerFilePath("file:///tmp/my%20file.txt")

	require.NoError(t, err)
	require.Equal(t, "/tmp/my file.txt", path)
	require.False(t, isDir)
}

func TestContainerFilePathMarksATrailingSlashAsADirectory(t *testing.T) {
	path, isDir, err := containerFilePath("file:///out/zarr_test/")

	require.NoError(t, err)
	require.Equal(t, "/out/zarr_test/", path)
	require.True(t, isDir)
}

func TestContainerFilePathRefusesTheContainerRoot(t *testing.T) {
	_, _, err := containerFilePath("file:///")

	require.Error(t, err)
	require.Contains(t, err.Error(), "root of the container")
}

func TestContainerFilePathRejectsAFileIRIWithAHost(t *testing.T) {
	_, _, err := containerFilePath("file://host/tmp/test.txt")

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
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Name: "test.txt", Modified: testFileModified, Digest: testFileDigest(), BlobPath: testFileDigest(), Path: filepath.Join(t.TempDir(), testFileDigest())}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{file}, t.TempDir()))

	copyIRI := "urn:sha256:" + testFileDigest()
	require.ElementsMatch(t, []string{
		"https://example.test/project/dataset https://schema.org/hasPart " + copyIRI,
		`https://example.test/project/dataset https://schema.org/name "a dataset"`,
		copyIRI + ` http://www.w3.org/2000/01/rdf-schema#label "test.txt"`,
		copyIRI + ` http://purl.org/dc/terms/modified "2026-09-10T14:26:05Z"`,
		copyIRI + ` http://purl.org/dc/terms/identifier "` + testFileDigest() + `"`,
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

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{file}, filepath.Dir(path)))

	require.NoFileExists(t, path)
	require.Equal(t, []string{`https://example.test/project/dataset https://schema.org/url "file:///tmp/test.txt"`}, graphTriples(graph))
}

func TestLinkCopiedFilesRejectsAFileObjectThatWasNotCopied(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file://host/tmp/test.txt> .
	`)

	err := LinkCopiedFiles(graph, nil, t.TempDir())

	require.Error(t, err)
	require.Contains(t, err.Error(), "file://host/tmp/test.txt")
}

// testTreeDigest is the digest of a directory holding a.txt and sub/b.txt with
// the contents used below, computed the way treeDigest documents.
func testTreeDigest(t *testing.T, files map[string]string) string {
	t.Helper()
	var entries []treeEntry
	for relative, content := range files {
		sum := sha256.Sum256([]byte(content))
		entries = append(entries, treeEntry{path: relative, digest: hex.EncodeToString(sum[:])})
	}
	return treeDigest(entries)
}

func TestRunTaskCopiesADirectoryVerbatimUnderItsOwnPath(t *testing.T) {
	blobDir := filepath.Join(t.TempDir(), "blobs")
	runner := &fakeRunner{
		ontology:  testOntology,
		runOutput: `{"@id":"dataset","schema:hasPart":{"@id":"file:///out/zarr_test/"}}` + "\n",
		containerFiles: map[string]string{
			"/out/zarr_test/a.txt":     "a",
			"/out/zarr_test/sub/b.txt": "b",
			"/out/other.txt":           "not part of the directory",
		},
	}

	result, err := runTestTask(t, runner, blobDir)

	require.NoError(t, err)
	require.Equal(t, []string{"/out/zarr_test/"}, runner.copies)
	require.Equal(t, []CopiedFile{{
		ContainerPath: "/out/zarr_test/",
		Directory:     true,
		Name:          "zarr_test",
		Modified:      testFileModified,
		Digest:        testTreeDigest(t, map[string]string{"a.txt": "a", "sub/b.txt": "b"}),
		BlobPath:      "out/zarr_test/",
		Path:          filepath.Join(blobDir, "out", "zarr_test"),
	}}, result.Files)
	a, err := os.ReadFile(filepath.Join(blobDir, "out", "zarr_test", "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "a", string(a))
	b, err := os.ReadFile(filepath.Join(blobDir, "out", "zarr_test", "sub", "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "b", string(b))
	// nothing of the copy is left under a temporary name
	entries, err := os.ReadDir(blobDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "out", entries[0].Name())
}

// A directory copied again lands at the same path, replacing what an earlier
// copy left there, since the path names the current copy and the digest is
// what tells the versions apart.
func TestRunTaskReplacesAnEarlierCopyOfADirectory(t *testing.T) {
	blobDir := filepath.Join(t.TempDir(), "blobs")
	require.NoError(t, os.MkdirAll(filepath.Join(blobDir, "zarr_test"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, "zarr_test", "stale.txt"), []byte("old"), 0644))
	runner := &fakeRunner{
		ontology:       testOntology,
		runOutput:      `{"@id":"dataset","schema:hasPart":{"@id":"file:///zarr_test/"}}` + "\n",
		containerFiles: map[string]string{"/zarr_test/a.txt": "a"},
	}

	_, err := runTestTask(t, runner, blobDir)

	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(blobDir, "zarr_test", "stale.txt"))
	require.FileExists(t, filepath.Join(blobDir, "zarr_test", "a.txt"))
}

func TestRunTaskFailsWhenANamedDirectoryCannotBeCopied(t *testing.T) {
	blobDir := t.TempDir()
	runner := &fakeRunner{
		ontology:  testOntology,
		runOutput: `{"@id":"a","schema:hasPart":{"@id":"file:///missing/"}}` + "\n",
	}

	_, err := runTestTask(t, runner, blobDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "no such directory /missing/")
	entries, err := os.ReadDir(blobDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// The digest of a directory depends on the paths and contents of its files
// alone, in whatever order they arrived.
func TestTreeDigestIsIndependentOfEntryOrder(t *testing.T) {
	forward := treeDigest([]treeEntry{{path: "a", digest: "1"}, {path: "b", digest: "2"}})
	backward := treeDigest([]treeEntry{{path: "b", digest: "2"}, {path: "a", digest: "1"}})

	require.Equal(t, forward, backward)
	sum := sha256.Sum256([]byte("a\x001\nb\x002\n"))
	require.Equal(t, hex.EncodeToString(sum[:]), forward)
	require.NotEqual(t, forward, treeDigest([]treeEntry{{path: "a", digest: "1"}, {path: "c", digest: "2"}}))
}

// A module may describe a file it produces with properties of its own; they
// stay with the file under its new name.
func TestLinkCopiedFilesRewritesAFileSubjectAndKeepsItsProperties(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file:///out/zarr_test/> .
		<file:///out/zarr_test/> schema:name "Test zarr project" .
	`)
	blobDir := t.TempDir()
	digest := testTreeDigest(t, map[string]string{"a.txt": "a"})
	dir := CopiedFile{ContainerPath: "/out/zarr_test/", Directory: true, Name: "zarr_test", Modified: testFileModified, Digest: digest, BlobPath: "out/zarr_test/", Path: filepath.Join(blobDir, "out", "zarr_test")}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{dir}, blobDir))

	copyIRI := "urn:sha256:" + digest
	require.ElementsMatch(t, []string{
		"https://example.test/project/dataset https://schema.org/hasPart " + copyIRI,
		copyIRI + ` https://schema.org/name "Test zarr project"`,
		copyIRI + ` http://www.w3.org/2000/01/rdf-schema#label "zarr_test"`,
		copyIRI + ` http://purl.org/dc/terms/modified "2026-09-10T14:26:05Z"`,
		copyIRI + ` http://purl.org/dc/terms/identifier "out/zarr_test/"`,
		copyIRI + " http://www.w3.org/2002/07/owl#versionIRI " + copyIRI,
	}, graphTriples(graph))
}

// A file the task only ever wrote as a subject is still a reference; the copy
// is kept and described.
func TestLinkCopiedFilesCountsASubjectAsAReference(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<file:///tmp/test.txt> schema:name "described but not linked" .
	`)
	path := filepath.Join(t.TempDir(), testFileDigest())
	require.NoError(t, os.WriteFile(path, []byte(testFileContent), 0644))
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Name: "test.txt", Digest: testFileDigest(), BlobPath: testFileDigest(), Path: path}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{file}, filepath.Dir(path)))

	require.FileExists(t, path)
	require.Contains(t, graphTriples(graph), "urn:sha256:"+testFileDigest()+` https://schema.org/name "described but not linked"`)
}

func TestLinkCopiedFilesRecordsADirectoryInProvenance(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file:///zarr_test/> .
	`)
	blobDir := t.TempDir()
	digest := testTreeDigest(t, map[string]string{"a.txt": "a"})
	dir := CopiedFile{ContainerPath: "/zarr_test/", Directory: true, Name: "zarr_test", Modified: testFileModified, Digest: digest, BlobPath: "zarr_test/", Path: filepath.Join(blobDir, "zarr_test")}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{dir}, blobDir))

	content, err := os.ReadFile(filepath.Join(blobDir, ProvFile))
	require.NoError(t, err)
	require.JSONEq(t, `{
		"@context": {
			"dcterms": "http://purl.org/dc/terms/",
			"owl": "http://www.w3.org/2002/07/owl#",
			"rdfs": "http://www.w3.org/2000/01/rdf-schema#",
			"xsd": "http://www.w3.org/2001/XMLSchema#"
		},
		"@graph": [{
			"@id": "zarr_test/",
			"rdfs:label": "zarr_test",
			"owl:versionIRI": {"@id": "urn:sha256:`+digest+`"},
			"dcterms:modified": {"@value": "2026-09-10T14:26:05Z", "@type": "xsd:dateTime"},
			"rdfs:comment": "Represents the directory zarr_test/ a SAL module task copied out of its container as of 2026-09-10T14:26:05Z. urn:sha256:`+digest+` is the SHA-256 of its contents, and the directory is served whole as a zip archive under that digest."
		}]
	}`, string(content))
	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	path, ok := provenance.DirectoryPath("urn:sha256:" + digest)
	require.True(t, ok)
	require.Equal(t, "zarr_test/", path)
	_, ok = provenance.DirectoryPath("urn:sha256:" + testFileDigest())
	require.False(t, ok)
}

// A single file is named by its digest and needs no record, so prov.jsonld is
// only written when a directory was copied.
func TestLinkCopiedFilesWritesNoProvenanceForFilesAlone(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file:///tmp/test.txt> .
	`)
	blobDir := t.TempDir()
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Name: "test.txt", Digest: testFileDigest(), BlobPath: testFileDigest(), Path: filepath.Join(blobDir, testFileDigest())}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{file}, blobDir))

	require.NoFileExists(t, filepath.Join(blobDir, ProvFile))
}

// A directory copied again replaces its earlier record rather than adding a
// second one, and other directories' records are kept.
func TestRecordDirectoriesReplacesTheRecordAtTheSamePath(t *testing.T) {
	blobDir := t.TempDir()
	first := CopiedFile{Directory: true, Name: "zarr_test", Modified: testFileModified, Digest: "1111", BlobPath: "zarr_test/"}
	other := CopiedFile{Directory: true, Name: "stac", Modified: testFileModified, Digest: "2222", BlobPath: "stac/"}
	require.NoError(t, recordDirectories(blobDir, []CopiedFile{first, other}))
	second := first
	second.Digest = "3333"

	require.NoError(t, recordDirectories(blobDir, []CopiedFile{second}))

	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	require.Len(t, provenance.nodes, 2)
	path, ok := provenance.DirectoryPath("3333")
	require.True(t, ok)
	require.Equal(t, "zarr_test/", path)
	_, ok = provenance.DirectoryPath("1111")
	require.False(t, ok)
	path, ok = provenance.DirectoryPath("2222")
	require.True(t, ok)
	require.Equal(t, "stac/", path)
}

func TestLinkCopiedFilesDiscardsAnUnreferencedDirectory(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:url "file:///zarr_test/" .
	`)
	blobDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(blobDir, "zarr_test"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, "zarr_test", "a.txt"), []byte("a"), 0644))
	dir := CopiedFile{ContainerPath: "/zarr_test/", Directory: true, Name: "zarr_test", Digest: "1111", BlobPath: "zarr_test/", Path: filepath.Join(blobDir, "zarr_test")}

	require.NoError(t, LinkCopiedFiles(graph, []CopiedFile{dir}, blobDir))

	require.NoDirExists(t, filepath.Join(blobDir, "zarr_test"))
	require.NoFileExists(t, filepath.Join(blobDir, ProvFile))
}
