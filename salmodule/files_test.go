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

// testModuleIRI is the IRI a copy made by the test module's task is
// attributed to: its namespace without the trailing slash.
const testModuleIRI = "salmodule://www.github.com/test/history-getter"

func testFileDigest() string {
	sum := sha256.Sum256([]byte(testFileContent))
	return hex.EncodeToString(sum[:])
}

func runTestTask(t *testing.T, runner *fakeRunner, blobDir string) (TaskResult, error) {
	t.Helper()
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	return newTestResolver(runner).RunTask(context.Background(), ref, DefaultTaskInstanceEnvVar, "{}", blobDir, nil)
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
		Source:        testModuleIRI,
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
	require.Equal(t, "a.txt", filepath.Base(result.Files[0].ContainerPath))
	require.Equal(t, "b.txt", filepath.Base(result.Files[1].ContainerPath))
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

// linkTestFiles is LinkCopiedFiles for a test that only cares that it succeeds.
func linkTestFiles(t *testing.T, graph *rdflibgo.Graph, files []CopiedFile, blobDir string) {
	t.Helper()
	require.NoError(t, LinkCopiedFiles(graph, files, blobDir))
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
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Modified: testFileModified, Digest: testFileDigest(), BlobPath: testFileDigest(), Path: filepath.Join(t.TempDir(), testFileDigest())}

	linkTestFiles(t, graph, []CopiedFile{file}, t.TempDir())

	copyIRI := "urn:sha256:" + testFileDigest()
	require.ElementsMatch(t, []string{
		"https://example.test/project/dataset https://schema.org/hasPart " + copyIRI,
		`https://example.test/project/dataset https://schema.org/name "a dataset"`,
		copyIRI + ` http://www.w3.org/2000/01/rdf-schema#label "file:///tmp/test.txt"`,
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
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Digest: testFileDigest(), Path: path}

	linkTestFiles(t, graph, []CopiedFile{file}, filepath.Dir(path))

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

func TestRunTaskCopiesADirectoryVerbatimUnderItsDigest(t *testing.T) {
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
	digest := testTreeDigest(t, map[string]string{"a.txt": "a", "sub/b.txt": "b"})
	require.Equal(t, []CopiedFile{{
		ContainerPath: "/out/zarr_test/",
		Directory:     true,
		Source:        testModuleIRI,
		Modified:      testFileModified,
		Digest:        digest,
		BlobPath:      digest + "/",
		Path:          filepath.Join(blobDir, digest),
	}}, result.Files)
	a, err := os.ReadFile(filepath.Join(blobDir, digest, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "a", string(a))
	b, err := os.ReadFile(filepath.Join(blobDir, digest, "sub", "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "b", string(b))
	// nothing of the copy is left under a temporary name
	entries, err := os.ReadDir(blobDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, digest, entries[0].Name())
}

// A directory copied again with the same contents lands under the same
// digest and replaces what is there, so a stale entry left under that name
// never survives beside the fresh copy.
func TestRunTaskReplacesAnEarlierCopyOfADirectory(t *testing.T) {
	blobDir := filepath.Join(t.TempDir(), "blobs")
	digest := testTreeDigest(t, map[string]string{"a.txt": "a"})
	earlier := filepath.Join(blobDir, digest)
	require.NoError(t, os.MkdirAll(earlier, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(earlier, "stale.txt"), []byte("old"), 0644))
	runner := &fakeRunner{
		ontology:       testOntology,
		runOutput:      `{"@id":"dataset","schema:hasPart":{"@id":"file:///zarr_test/"}}` + "\n",
		containerFiles: map[string]string{"/zarr_test/a.txt": "a"},
	}

	_, err := runTestTask(t, runner, blobDir)

	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(earlier, "stale.txt"))
	require.FileExists(t, filepath.Join(earlier, "a.txt"))
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
	dir := CopiedFile{ContainerPath: "/out/zarr_test/", Directory: true, Source: testModuleIRI, Modified: testFileModified, Digest: digest, BlobPath: digest + "/", Path: filepath.Join(blobDir, digest)}

	linkTestFiles(t, graph, []CopiedFile{dir}, blobDir)

	copyIRI := "urn:sha256:" + digest
	require.ElementsMatch(t, []string{
		"https://example.test/project/dataset https://schema.org/hasPart " + copyIRI,
		copyIRI + ` https://schema.org/name "Test zarr project"`,
		copyIRI + ` http://www.w3.org/2000/01/rdf-schema#label "file:///out/zarr_test/"`,
		copyIRI + ` http://purl.org/dc/terms/modified "2026-09-10T14:26:05Z"`,
		copyIRI + ` http://purl.org/dc/terms/identifier "` + digest + `/"`,
		copyIRI + " http://purl.org/dc/terms/source " + testModuleIRI,
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
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Digest: testFileDigest(), BlobPath: testFileDigest(), Path: path}

	linkTestFiles(t, graph, []CopiedFile{file}, filepath.Dir(path))

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
	dir := CopiedFile{ContainerPath: "/zarr_test/", Directory: true, Source: testModuleIRI, Modified: testFileModified, Digest: digest, BlobPath: digest + "/", Path: filepath.Join(blobDir, digest)}

	linkTestFiles(t, graph, []CopiedFile{dir}, blobDir)

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
			"@id": "urn:sha256:`+digest+`",
			"rdfs:label": "file:///zarr_test/",
			"owl:versionIRI": {"@id": "urn:sha256:`+digest+`"},
			"dcterms:identifier": "`+digest+`/",
			"dcterms:source": {"@id": "`+testModuleIRI+`"},
			"dcterms:modified": {"@value": "2026-09-10T14:26:05Z", "@type": "xsd:dateTime"},
			"rdfs:comment": "Represents the directory file:///zarr_test/ a task of the SAL module `+testModuleIRI+` copied out of its container as of 2026-09-10T14:26:05Z. urn:sha256:`+digest+` is the SHA-256 of its contents, and the directory is served whole as a zip archive under that digest."
		}]
	}`, string(content))
	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	require.Equal(t, []string{digest + "/"}, provenance.BlobPaths())
}

func TestProvenanceLabelIsTheFileIRIOfTheCopyAtAPath(t *testing.T) {
	blobDir := t.TempDir()
	seedCopiedDirectory(t, blobDir, "/out/catalog/", "1111")
	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)

	label, ok := provenance.Label("1111/")

	require.True(t, ok)
	require.Equal(t, "file:///out/catalog/", label)
	_, ok = provenance.Label("2222/")
	require.False(t, ok)
}

// A single file is recorded the same way, so that the log says which module
// produced it.
func TestLinkCopiedFilesRecordsAFileInProvenance(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file:///tmp/test.txt> .
	`)
	blobDir := t.TempDir()
	file := CopiedFile{ContainerPath: "/tmp/test.txt", Source: testModuleIRI, Modified: testFileModified, Digest: testFileDigest(), BlobPath: testFileDigest(), Path: filepath.Join(blobDir, testFileDigest())}

	linkTestFiles(t, graph, []CopiedFile{file}, blobDir)

	content, err := os.ReadFile(filepath.Join(blobDir, ProvFile))
	require.NoError(t, err)
	require.Contains(t, string(content), `"@id": "urn:sha256:`+testFileDigest()+`"`)
	require.Contains(t, string(content), `"dcterms:identifier": "`+testFileDigest()+`"`)
	require.Contains(t, string(content), `"dcterms:source": {`)
	require.Contains(t, string(content), `"rdfs:comment": "Represents the file file:///tmp/test.txt a task of the SAL module `+testModuleIRI+` copied out of its container as of 2026-09-10T14:26:05Z. urn:sha256:`+testFileDigest()+` is the SHA-256 of its contents, and the file is served under that digest."`)
	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	require.Equal(t, []string{testFileDigest()}, provenance.BlobPaths())
}

// prov.jsonld is a log: a copy with a digest already recorded replaces that
// record, since it is the same contents, and every other record is kept,
// including an earlier version of a directory at the same container path.
func TestRecordCopiesReplacesTheRecordOfTheSameDigestAndKeepsTheRest(t *testing.T) {
	blobDir := t.TempDir()
	first := CopiedFile{ContainerPath: "/zarr_test/", Directory: true, Modified: testFileModified, Digest: "1111", BlobPath: "1111/"}
	other := CopiedFile{ContainerPath: "/stac/", Directory: true, Modified: testFileModified, Digest: "2222", BlobPath: "2222/"}
	require.NoError(t, recordCopies(blobDir, []CopiedFile{first, other}))
	newer := first
	newer.Digest = "3333"
	newer.BlobPath = "3333/"

	require.NoError(t, recordCopies(blobDir, []CopiedFile{first, newer}))

	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	require.Equal(t, []string{"1111/", "2222/", "3333/"}, provenance.BlobPaths())
	label, ok := provenance.Label("3333/")
	require.True(t, ok)
	require.Equal(t, "file:///zarr_test/", label)
}

// A directory an earlier run copied is left on disk and in prov.jsonld when a
// later run copies something else; only the user clears the blob store.
func TestLinkCopiedFilesLeavesEarlierCopiesAlone(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file:///out/catalog/> .
	`)
	blobDir := t.TempDir()
	earlier := seedCopiedDirectory(t, blobDir, "/out/catalog/", "1111")
	current := seedCopiedDirectory(t, blobDir, "/out/catalog/", "2222")

	linkTestFiles(t, graph, []CopiedFile{current}, blobDir)

	require.DirExists(t, earlier.Path)
	require.DirExists(t, current.Path)
	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	require.Equal(t, []string{"1111/", "2222/"}, provenance.BlobPaths())
}

func TestLinkCopiedFilesDiscardsAnUnreferencedDirectory(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:url "file:///zarr_test/" .
	`)
	blobDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(blobDir, "1111"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, "1111", "a.txt"), []byte("a"), 0644))
	dir := CopiedFile{ContainerPath: "/zarr_test/", Directory: true, Digest: "1111", BlobPath: "1111/", Path: filepath.Join(blobDir, "1111")}

	linkTestFiles(t, graph, []CopiedFile{dir}, blobDir)

	require.NoDirExists(t, filepath.Join(blobDir, "1111"))
	require.NoFileExists(t, filepath.Join(blobDir, ProvFile))
}

// A copy's record in prov.jsonld describes it in a graph the same way
// LinkCopiedFiles did when it was copied, with the record's comment as well,
// so a table built again from source still describes the copy.
func TestProvenanceAppendProvenanceDescribesEveryRecordedCopy(t *testing.T) {
	blobDir := t.TempDir()
	dir := CopiedFile{ContainerPath: "/out/catalog/", Directory: true, Source: testModuleIRI, Modified: testFileModified, Digest: "1111", BlobPath: "1111/"}
	require.NoError(t, recordCopies(blobDir, []CopiedFile{dir}))
	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	graph := rdflibgo.NewGraph()

	require.Equal(t, 1, provenance.AppendProvenance(graph))

	subject := rdflibgo.NewURIRefUnsafe("urn:sha256:1111")
	require.True(t, graph.Contains(subject, rdflibgo.NewURIRefUnsafe(owlVersionIRI), subject))
	require.True(t, graph.Contains(subject, rdflibgo.NewURIRefUnsafe(rdfsLabelIRI), rdflibgo.NewLiteral("file:///out/catalog/")))
	require.True(t, graph.Contains(subject, rdflibgo.NewURIRefUnsafe(dctermsIdentifierIRI), rdflibgo.NewLiteral("1111/")))
	require.True(t, graph.Contains(subject, rdflibgo.NewURIRefUnsafe(dctermsSourceIRI), rdflibgo.NewURIRefUnsafe(testModuleIRI)))
	require.True(t, graph.Contains(subject, rdflibgo.NewURIRefUnsafe(dctermsModifiedIRI), rdflibgo.NewLiteral("2026-09-10T14:26:05Z", rdflibgo.WithDatatype(rdflibgo.XSDDateTime))))
	require.Contains(t, graphTriples(graph), `urn:sha256:1111 `+rdfsCommentIRI+` "Represents the directory file:///out/catalog/ a task of the SAL module `+testModuleIRI+` copied out of its container as of 2026-09-10T14:26:05Z. urn:sha256:1111 is the SHA-256 of its contents, and the directory is served whole as a zip archive under that digest."`)
	require.Len(t, graphTriples(graph), 6)
}

// Appending the record of a copy the graph already describes, because this
// run made it, adds only the statements the graph lacks.
func TestProvenanceAppendProvenanceAddsNothingTwice(t *testing.T) {
	blobDir := t.TempDir()
	dir := CopiedFile{ContainerPath: "/out/catalog/", Directory: true, Source: testModuleIRI, Modified: testFileModified, Digest: "1111", BlobPath: "1111/"}
	require.NoError(t, recordCopies(blobDir, []CopiedFile{dir}))
	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	graph := rdflibgo.NewGraph()
	provenance.AppendProvenance(graph)

	provenance.AppendProvenance(graph)

	require.Len(t, graphTriples(graph), 6)
}

func TestProvenanceAppendProvenanceIsEmptyWithoutAProvFile(t *testing.T) {
	provenance, err := LoadProvenance(t.TempDir())
	require.NoError(t, err)
	graph := rdflibgo.NewGraph()

	require.Zero(t, provenance.AppendProvenance(graph))
	require.Empty(t, graphTriples(graph))
}

// seedCopiedDirectory puts a directory on disk under blobDir and records it in
// prov.jsonld the way a run does, standing in for a copy an earlier run made.
func seedCopiedDirectory(t *testing.T, blobDir string, containerPath string, digest string) CopiedFile {
	t.Helper()
	dir := CopiedFile{ContainerPath: containerPath, Directory: true, Source: testModuleIRI, Modified: testFileModified, Digest: digest, BlobPath: digest + "/", Path: filepath.Join(blobDir, digest)}
	require.NoError(t, os.MkdirAll(dir.Path, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir.Path, "a.txt"), []byte("a"), 0644))
	require.NoError(t, recordCopies(blobDir, []CopiedFile{dir}))
	return dir
}

// Only the copies that are kept are recorded; a discarded copy leaves no
// record behind.
func TestLinkCopiedFilesRecordsOnlyTheCopiesItKept(t *testing.T) {
	graph := parseTestTurtle(t, `
		@prefix schema: <https://schema.org/> .
		<dataset> schema:hasPart <file:///tmp/test.txt> ;
			schema:url "file:///tmp/dropped.txt" .
	`)
	blobDir := t.TempDir()
	referenced := CopiedFile{ContainerPath: "/tmp/test.txt", Digest: testFileDigest(), BlobPath: testFileDigest(), Path: filepath.Join(blobDir, testFileDigest())}
	dropped := CopiedFile{ContainerPath: "/tmp/dropped.txt", Digest: "2222", BlobPath: "2222", Path: filepath.Join(blobDir, "2222")}

	linkTestFiles(t, graph, []CopiedFile{referenced, dropped}, blobDir)

	provenance, err := LoadProvenance(blobDir)
	require.NoError(t, err)
	require.Equal(t, []string{testFileDigest()}, provenance.BlobPaths())
}

func TestRemoveCopiedFilesDeletesEveryRecordedCopyAndTheFile(t *testing.T) {
	blobDir := t.TempDir()
	first := seedCopiedDirectory(t, blobDir, "/out/catalog/", "1111")
	second := seedCopiedDirectory(t, blobDir, "/zarr/", "2222")
	recorded := CopiedFile{ContainerPath: "/tmp/test.txt", Source: testModuleIRI, Modified: testFileModified, Digest: "3333", BlobPath: "3333", Path: filepath.Join(blobDir, "3333")}
	require.NoError(t, os.WriteFile(recorded.Path, []byte("a copied file"), 0644))
	require.NoError(t, recordCopies(blobDir, []CopiedFile{recorded}))
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, "4444"), []byte("a pinned vocabulary document"), 0644))

	removed, err := RemoveCopiedFiles(blobDir)

	require.NoError(t, err)
	require.Equal(t, []string{"1111/", "2222/", "3333"}, removed)
	require.NoDirExists(t, first.Path)
	require.NoDirExists(t, second.Path)
	require.NoFileExists(t, recorded.Path)
	require.NoFileExists(t, filepath.Join(blobDir, ProvFile))
	require.FileExists(t, filepath.Join(blobDir, "4444"))
}

func TestRemoveCopiedFilesDoesNothingWithoutAProvFile(t *testing.T) {
	removed, err := RemoveCopiedFiles(t.TempDir())

	require.NoError(t, err)
	require.Empty(t, removed)
}

// A record without an identifier names its copy by the digest of its @id,
// which is the name everything in the blob store is stored under.
func TestRemoveCopiedFilesReadsARecordWithoutAnIdentifier(t *testing.T) {
	blobDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, "1111"), []byte("a copied file"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, ProvFile), []byte(`{"@context": {}, "@graph": [{"@id": "urn:sha256:1111", "owl:versionIRI": {"@id": "urn:sha256:1111"}}]}`), 0644))

	removed, err := RemoveCopiedFiles(blobDir)

	require.NoError(t, err)
	require.Equal(t, []string{"1111"}, removed)
	require.NoFileExists(t, filepath.Join(blobDir, "1111"))
}

func TestRemoveCopiedFilesRefusesAPathOutsideTheBlobStore(t *testing.T) {
	blobDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blobDir, ProvFile), []byte(`{"@context": {}, "@graph": [{"@id": "urn:sha256:1111", "dcterms:identifier": "../escape/", "owl:versionIRI": {"@id": "urn:sha256:1111"}}]}`), 0644))

	_, err := RemoveCopiedFiles(blobDir)

	require.ErrorContains(t, err, "not inside the blob store")
}
