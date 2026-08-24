package salmodule

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

const testModuleCommitHash = "abc123def456abc123def456abc123def456abc"

type recordedRun struct {
	image string
	env   []string
	cmd   []string
}

// fakeRunner stands in for the docker daemon so module resolution can be tested
// without building or running a container.
type fakeRunner struct {
	ontology  string
	runOutput string
	// existingImages are the tags ImageExists reports as already on the daemon.
	existingImages []string
	builds         []string
	runs           []recordedRun
}

func (f *fakeRunner) BuildImage(_ context.Context, _ string, tag string) error {
	f.builds = append(f.builds, tag)
	return nil
}

func (f *fakeRunner) ImageExists(_ context.Context, tag string) (bool, error) {
	return slices.Contains(f.existingImages, tag), nil
}

func (f *fakeRunner) RunContainer(_ context.Context, image string, env []string, cmd []string) ([]byte, []byte, error) {
	f.runs = append(f.runs, recordedRun{image: image, env: env, cmd: cmd})
	switch cmd[len(cmd)-1] {
	case OntologyCommand:
		return []byte(f.ontology), nil, nil
	case RunCommand:
		return []byte(f.runOutput), nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected command %v", cmd)
}

// fakeClone populates the clone destination the way git would, so the resolver
// finds the Dockerfile it expects in the repository root, and answers a
// rev-parse HEAD with a fixed commit hash.
func fakeClone(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
	if args[0] == "rev-parse" {
		return []byte(testModuleCommitHash), nil
	}
	destination := args[len(args)-1]
	return nil, os.WriteFile(filepath.Join(destination, "Dockerfile"), []byte("FROM scratch\n"), 0644)
}

func newTestResolver(runner *fakeRunner) *Resolver {
	return &Resolver{Runner: runner, Command: fakeClone}
}

func TestResolverBuildsModuleOntology(t *testing.T) {
	runner := &fakeRunner{ontology: testOntology}
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)

	ontology, err := newTestResolver(runner).Ontology(context.Background(), ref)

	require.NoError(t, err)
	require.True(t, ontology.IsTaskClass(testModuleNamespace+"EducationalHistoryFinder"))
	require.Equal(t, []string{ref.ImageTagFor(testModuleCommitHash)}, runner.builds)
	require.Equal(t, []string{BaseCommand, OntologyCommand}, runner.runs[0].cmd)
}

func TestResolverBuildsEachModuleOnlyOnce(t *testing.T) {
	runner := &fakeRunner{ontology: testOntology, runOutput: ""}
	resolver := newTestResolver(runner)
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)

	_, err = resolver.Ontology(context.Background(), ref)
	require.NoError(t, err)
	_, err = resolver.Ontology(context.Background(), ref)
	require.NoError(t, err)
	_, err = resolver.RunTask(context.Background(), ref, DefaultTaskInstanceEnvVar, "{}")
	require.NoError(t, err)

	require.Len(t, runner.builds, 1)
	// the cached ontology means only the ontology and run commands were invoked
	require.Len(t, runner.runs, 2)
}

// A commit pinned in .sal/config.jsonld names the exact image tag a previous
// invocation built, so finding it on the daemon skips the clone entirely.
func TestResolverReusesThePinnedImageWithoutCloning(t *testing.T) {
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	runner := &fakeRunner{
		ontology:       testOntology,
		existingImages: []string{ref.ImageTagFor(testModuleCommitHash)},
	}
	resolver := &Resolver{
		Runner: runner,
		Command: func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, fmt.Errorf("git must not run when the pinned image exists")
		},
	}
	resolver.UsePinnedCommit(ref.Namespace, testModuleCommitHash)

	_, err = resolver.Ontology(context.Background(), ref)

	require.NoError(t, err)
	require.Empty(t, runner.builds)
	require.Equal(t, ref.ImageTagFor(testModuleCommitHash), runner.runs[0].image)
	// a reused module still counts as one this invocation resolved, so it is
	// still recorded on the table it helps produce
	require.Equal(t, []string{"salmodule://www.github.com/test/history-getter"}, resolver.Downloaded())

	hash, err := resolver.CommitHash(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, testModuleCommitHash, hash)
}

// A pinned commit whose image is no longer on the daemon falls back to the
// normal clone and build, rather than failing or trusting the pin blindly.
func TestResolverClonesAndBuildsWhenThePinnedImageIsMissing(t *testing.T) {
	runner := &fakeRunner{ontology: testOntology}
	resolver := newTestResolver(runner)
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	resolver.UsePinnedCommit(ref.Namespace, testModuleCommitHash)

	_, err = resolver.Ontology(context.Background(), ref)

	require.NoError(t, err)
	require.Equal(t, []string{ref.ImageTagFor(testModuleCommitHash)}, runner.builds)
}

// Even without a pin, a clone whose HEAD was already built and tagged by an
// earlier invocation skips the docker build.
func TestResolverSkipsTheBuildWhenTheClonedCommitsImageExists(t *testing.T) {
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)
	runner := &fakeRunner{
		ontology:       testOntology,
		existingImages: []string{ref.ImageTagFor(testModuleCommitHash)},
	}

	_, err = newTestResolver(runner).Ontology(context.Background(), ref)

	require.NoError(t, err)
	require.Empty(t, runner.builds)
	require.Equal(t, ref.ImageTagFor(testModuleCommitHash), runner.runs[0].image)
}

func TestResolverCommitHashReturnsTheClonedRepositorysHead(t *testing.T) {
	runner := &fakeRunner{ontology: testOntology}
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)

	hash, err := newTestResolver(runner).CommitHash(context.Background(), ref)

	require.NoError(t, err)
	require.Equal(t, testModuleCommitHash, hash)
}

func TestFetchOntologyDocumentReportsTheCommitHashOfTheModuleItBuilt(t *testing.T) {
	resolver := Default()
	originalRunner, originalCommand := resolver.Runner, resolver.Command
	resolver.Reset()
	resolver.Runner = &fakeRunner{ontology: testOntology}
	resolver.Command = fakeClone
	t.Cleanup(func() {
		resolver.Runner, resolver.Command = originalRunner, originalCommand
		resolver.Reset()
	})

	_, mediaType, commitHash, err := FetchOntologyDocument(testModuleNamespace)

	require.NoError(t, err)
	require.Equal(t, "application/ld+json", mediaType)
	require.Equal(t, testModuleCommitHash, commitHash)
}

func TestResolverReportsDownloadedModules(t *testing.T) {
	runner := &fakeRunner{ontology: testOntology}
	resolver := newTestResolver(runner)
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)

	require.Empty(t, resolver.Downloaded())
	_, err = resolver.Ontology(context.Background(), ref)
	require.NoError(t, err)

	// the module URI is the namespace without the trailing slash a vocabulary base has
	require.Equal(t, []string{"salmodule://www.github.com/test/history-getter"}, resolver.Downloaded())
}

func TestResolverRunTaskPassesTaskInstanceThroughEnvironment(t *testing.T) {
	runner := &fakeRunner{ontology: testOntology}
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)

	_, err = newTestResolver(runner).RunTask(context.Background(), ref, "MODULE_TASK", `{"@id":"x"}`)

	require.NoError(t, err)
	require.Equal(t, []string{`MODULE_TASK={"@id":"x"}`}, runner.runs[0].env)
	require.Equal(t, []string{BaseCommand, RunCommand}, runner.runs[0].cmd)
}

func TestResolverRejectsModuleWithoutDockerfile(t *testing.T) {
	resolver := &Resolver{
		Runner:  &fakeRunner{},
		Command: func(context.Context, string, string, ...string) ([]byte, error) { return nil, nil },
	}
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)

	_, err = resolver.Ontology(context.Background(), ref)

	require.Error(t, err)
	require.Contains(t, err.Error(), "has no Dockerfile in its repository root")
}

func TestResolverReportsCloneFailures(t *testing.T) {
	resolver := &Resolver{
		Runner: &fakeRunner{},
		Command: func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, fmt.Errorf("repository not found")
		},
	}
	ref, err := ParseModuleIRI(testModuleNamespace)
	require.NoError(t, err)

	_, err = resolver.Ontology(context.Background(), ref)

	require.Error(t, err)
	require.Contains(t, err.Error(), "clone SAL module")
	require.Contains(t, err.Error(), "repository not found")
}
