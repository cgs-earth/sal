package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const pinsTestVocabulary = `@prefix owl: <http://www.w3.org/2002/07/owl#> .
<https://vocab.test/things#Widget> a owl:Class .
<https://vocab.test/things#label> a owl:DatatypeProperty .
`

const pinsTestSource = `@prefix things: <https://vocab.test/things#> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

<widgets/1> a things:Widget ;
    things:label "A widget" .
`

// newPinsTestProject creates a committed SAL project holding one Turtle file
// and makes it the working directory, so that a build runs against a real git
// repository the way the command does.
func newPinsTestProject(t *testing.T) string {
	t.Helper()

	project := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = project
		out, err := command.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}

	git("init")
	git("remote", "add", "origin", "https://github.com/cgs-earth/sal-pins-test-project.git")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal", "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"), []byte(".sal/data\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(project, "data.ttl"), []byte(pinsTestSource), 0644))
	git("add", "-A")
	git("-c", "user.email=sal@example.test", "-c", "user.name=SAL", "commit", "-m", "project source")

	t.Chdir(project)
	return project
}

// servePinsTestVocabulary makes the project's vocabulary resolvable without
// reaching the network, and reports how many times it was dereferenced.
func servePinsTestVocabulary(t *testing.T) *int {
	t.Helper()

	fetches := 0
	original := projectVocabularies
	projectVocabularies = func(refresh bool) (*validate.PinnedVocabularies, error) {
		pins, err := original(refresh)
		if err != nil {
			return nil, err
		}
		pins.Fetch = func(source string) ([]byte, string, validate.PinnedVersion, error) {
			fetches++
			if source != "https://vocab.test/things" {
				return nil, "", validate.PinnedVersion{}, fmt.Errorf("bad response status code: 404")
			}
			return []byte(pinsTestVocabulary), "text/turtle", validate.PinnedVersion{}, nil
		}
		return pins, nil
	}
	t.Cleanup(func() { projectVocabularies = original })
	return &fetches
}

func TestBuildPinsEveryVocabularyItResolved(t *testing.T) {
	project := newPinsTestProject(t)
	fetches := servePinsTestVocabulary(t)

	graph, err := (&BuildCmd{Format: GraphExportFormatNQuads}).Run()
	require.NoError(t, err)

	require.Equal(t, 1, *fetches)
	content, err := os.ReadFile(filepath.Join(project, ".sal", "config.jsonld"))
	require.NoError(t, err)
	require.Contains(t, string(content), `"@id": "https://vocab.test/things#"`)
	require.Contains(t, string(content), "urn:sha256:")
	// the XSD built-ins are checked without a vocabulary document, so the prefix
	// the source declares for them has no version to pin
	require.NotContains(t, string(content), `"@id": "http://www.w3.org/2001/XMLSchema#"`)

	// the pin is also carried as provenance in the built graph itself, not only
	// in the lockfile on disk
	var versionIRIs int
	subject := rdflibgo.NewURIRefUnsafe("https://vocab.test/things#")
	predicate := rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#versionIRI")
	graph.Triples(subject, &predicate, nil)(func(rdflibgo.Triple) bool {
		versionIRIs++
		return true
	})
	require.Equal(t, 1, versionIRIs)

	pins, err := validate.LoadPinnedVocabularies(filepath.Join(project, ".sal", "config.jsonld"), filepath.Join(project, ".sal", "data"))
	require.NoError(t, err)
	documents := pins.Documents()
	require.Len(t, documents, 1)
	document, err := os.ReadFile(documents[0])
	require.NoError(t, err)
	require.Equal(t, pinsTestVocabulary, string(document))
}

// A second build resolves everything from the pins, which is both why it makes
// no requests and why it must leave the lockfile alone: rewriting it would
// dirty the worktree and the build after it would refuse to run.
func TestASecondBuildResolvesFromThePinsAndRewritesNothing(t *testing.T) {
	project := newPinsTestProject(t)
	fetches := servePinsTestVocabulary(t)
	_, err := (&BuildCmd{Format: GraphExportFormatNQuads}).Run()
	require.NoError(t, err)

	lockfile := filepath.Join(project, ".sal", "config.jsonld")
	before, err := os.Stat(lockfile)
	require.NoError(t, err)

	_, err = (&BuildCmd{Format: GraphExportFormatNQuads, Force: true}).Run()
	require.NoError(t, err)

	require.Equal(t, 1, *fetches)
	after, err := os.Stat(lockfile)
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime())
}

func TestNoCacheResolvesThePinnedVocabularyAgain(t *testing.T) {
	newPinsTestProject(t)
	fetches := servePinsTestVocabulary(t)
	_, err := (&BuildCmd{Format: GraphExportFormatNQuads}).Run()
	require.NoError(t, err)

	_, err = (&BuildCmd{Format: GraphExportFormatNQuads, Force: true, NoCache: true}).Run()
	require.NoError(t, err)

	require.Equal(t, 2, *fetches)
}

// A build cannot pin a version of a vocabulary it cannot read, so a prefix that
// fails to resolve fails the build even when no term from it was used.
func TestBuildFailsWhenADeclaredPrefixCannotBeResolved(t *testing.T) {
	project := newPinsTestProject(t)
	servePinsTestVocabulary(t)
	require.NoError(t, os.WriteFile(filepath.Join(project, "data.ttl"), []byte(pinsTestSource+`
@prefix unused: <https://vocab.test/unused#> .
`), 0644))

	_, err := (&BuildCmd{Format: GraphExportFormatNQuads, Force: true}).Run()

	require.Error(t, err)
	require.Contains(t, err.Error(), "https://vocab.test/unused#")
	require.Contains(t, err.Error(), "404")
	require.NoFileExists(t, filepath.Join(project, ".sal", "config.jsonld"))
}
