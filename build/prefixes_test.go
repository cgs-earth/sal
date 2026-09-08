package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/stretchr/testify/require"
)

// answerPrompt replaces the terminal prompt for the test and records the
// questions asked.
func answerPrompt(t *testing.T, answer bool) *[]string {
	t.Helper()
	var asked []string
	original := confirm
	confirm = func(question string) bool {
		asked = append(asked, question)
		return answer
	}
	t.Cleanup(func() { confirm = original })
	return &asked
}

// writeBareNamespaceSource adds a prefix whose namespace ends in neither / nor
// # to the project's source, and serves a vocabulary document for it alongside
// the project's own so the build can pin it if it is accepted.
func writeBareNamespaceSource(t *testing.T, project string) *[]string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(project, "data.ttl"), []byte(pinsTestSource+`
@prefix bare: <https://vocab.test/bare> .
`), 0644))

	var fetched []string
	original := projectVocabularies
	projectVocabularies = func(refresh bool) (*validate.PinnedVocabularies, error) {
		pins, err := original(refresh)
		if err != nil {
			return nil, err
		}
		pins.Fetch = func(source string) ([]byte, string, validate.PinnedVersion, error) {
			fetched = append(fetched, source)
			if source != "https://vocab.test/things" && source != "https://vocab.test/bare" {
				return nil, "", validate.PinnedVersion{}, fmt.Errorf("bad response status code: 404")
			}
			return []byte(pinsTestVocabulary), "text/turtle", validate.PinnedVersion{}, nil
		}
		return pins, nil
	}
	t.Cleanup(func() { projectVocabularies = original })
	return &fetched
}

func TestBuildRefusesAPrefixWithoutTerminatorTheUserDeclines(t *testing.T) {
	project := newPinsTestProject(t)
	fetched := writeBareNamespaceSource(t, project)
	asked := answerPrompt(t, false)

	_, err := (&BuildCmd{Format: GraphExportFormatNQuads, Force: true}).Run()

	require.ErrorIs(t, err, ErrPrefixRejected)
	require.Len(t, *asked, 1)
	require.Contains(t, (*asked)[0], "<https://vocab.test/bare>")
	require.Contains(t, (*asked)[0], "data.ttl")
	// the question is asked before any term is checked, so nothing at all is
	// fetched for a build the user then refuses
	require.Empty(t, *fetched)
	require.NoFileExists(t, filepath.Join(project, ".sal", "config.jsonld"))
}

func TestBuildIncludesAPrefixWithoutTerminatorTheUserAccepts(t *testing.T) {
	project := newPinsTestProject(t)
	writeBareNamespaceSource(t, project)
	asked := answerPrompt(t, true)

	_, err := (&BuildCmd{Format: GraphExportFormatNQuads, Force: true}).Run()

	require.NoError(t, err)
	require.Len(t, *asked, 1)
	// an accepted prefix is pinned like any other
	content, err := os.ReadFile(filepath.Join(project, ".sal", "config.jsonld"))
	require.NoError(t, err)
	require.Contains(t, string(content), `"@id": "https://vocab.test/bare"`)
}

func TestYesIncludesAPrefixWithoutTerminatorWithoutAsking(t *testing.T) {
	project := newPinsTestProject(t)
	writeBareNamespaceSource(t, project)
	asked := answerPrompt(t, false)

	_, err := (&BuildCmd{Format: GraphExportFormatNQuads, Force: true, Yes: true}).Run()

	require.NoError(t, err)
	require.Empty(t, *asked)
}

func TestValidateAsksAboutAPrefixWithoutTerminatorAndYesSkipsTheQuestion(t *testing.T) {
	project := newPinsTestProject(t)
	writeBareNamespaceSource(t, project)
	// validate has no --force, so the source has to be committed
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=sal@example.test", "-c", "user.name=SAL", "commit", "-m", "bare prefix"}} {
		command := exec.Command("git", args...)
		command.Dir = project
		out, err := command.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	asked := answerPrompt(t, false)

	_, err := (&ValidateCmd{Paths: []string{project}}).Run()
	require.ErrorIs(t, err, ErrPrefixRejected)
	require.Len(t, *asked, 1)

	_, err = (&ValidateCmd{Paths: []string{project}, Yes: true}).Run()
	require.NoError(t, err)
	require.Len(t, *asked, 1)
}

func TestBuildDoesNotAskAboutWellFormedPrefixes(t *testing.T) {
	newPinsTestProject(t)
	servePinsTestVocabulary(t)
	asked := answerPrompt(t, false)

	_, err := (&BuildCmd{Format: GraphExportFormatNQuads}).Run()

	require.NoError(t, err)
	require.Empty(t, *asked)
}

func TestBuildRejectsTheSameVocabularyDeclaredUnderHttpAndHttps(t *testing.T) {
	project := newPinsTestProject(t)
	servePinsTestVocabulary(t)
	require.NoError(t, os.WriteFile(filepath.Join(project, "more.ttl"), []byte(`
@prefix things: <http://vocab.test/things#> .

<widgets/2> a things:Widget .
`), 0644))

	_, err := (&BuildCmd{Format: GraphExportFormatNQuads, Force: true}).Run()

	require.ErrorIs(t, err, ErrConflictingPrefixes)
	require.NoFileExists(t, filepath.Join(project, ".sal", "config.jsonld"))
}
