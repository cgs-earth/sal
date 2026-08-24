package salmodule

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// CommandRunner runs an external command and returns its combined output,
// allowing git usage to be faked in tests.
type CommandRunner func(ctx context.Context, dir string, name string, args ...string) ([]byte, error)

func runCommand(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %s: %w", name, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

// Resolver dereferences salmodule:// IRIs by cloning the module's git
// repository, building the Dockerfile in its root, and invoking the SAL Module
// command line interface inside the resulting image.
//
// A module is cloned and built at most once per resolver.
type Resolver struct {
	// Runner runs docker operations. A client for the local docker daemon is
	// created on first use when this is nil.
	Runner ContainerRunner
	// Command runs git. os/exec is used when this is nil.
	Command CommandRunner

	mu         sync.Mutex
	images     map[string]string
	commits    map[string]string
	ontologies map[string]*ModuleOntology
	// pinnedCommits holds the git commit a project has pinned each module at in
	// .sal/config.jsonld. A pinned module whose image is still on the docker
	// daemon is reused without being cloned or built again.
	pinnedCommits map[string]string
}

var defaultResolver = &Resolver{}

// Default returns the resolver shared by validation and build so that a module
// referenced from several places is only cloned and built once per invocation.
func Default() *Resolver { return defaultResolver }

// Reset drops every module the resolver has already cloned, built, and
// dereferenced so that the next reference resolves from scratch.
func (r *Resolver) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images = nil
	r.commits = nil
	r.ontologies = nil
	r.pinnedCommits = nil
}

// UsePinnedCommit tells the resolver which git commit the project pins the
// module at, so that the image a previous invocation built and tagged with
// that commit can be reused instead of cloning and building the module again.
func (r *Resolver) UsePinnedCommit(namespace string, commit string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pinnedCommits == nil {
		r.pinnedCommits = map[string]string{}
	}
	r.pinnedCommits[namespace] = commit
}

// Downloaded returns the salmodule:// URI of every module the resolver has
// resolved to an image, whether it cloned and built the module or reused a
// prebuilt image, and whether it was dereferenced for its vocabulary or run as
// a task. A build records these so that a table says which modules produced it.
func (r *Resolver) Downloaded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	uris := make([]string, 0, len(r.images))
	for namespace := range r.images {
		// a namespace is a vocabulary base and so ends in a slash; the module
		// itself is named by the IRI without it
		uris = append(uris, strings.TrimSuffix(namespace, "/"))
	}
	slices.Sort(uris)
	return uris
}

// Ontology returns the vocabulary the module publishes through its ontology command.
func (r *Resolver) Ontology(ctx context.Context, ref ModuleRef) (*ModuleOntology, error) {
	r.mu.Lock()
	cached, ok := r.ontologies[ref.Namespace]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	stdout, err := r.runModuleCommand(ctx, ref, nil, OntologyCommand)
	if err != nil {
		return nil, err
	}
	ontology, err := parseModuleOntology(ref.Namespace, stdout)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ontologies == nil {
		r.ontologies = map[string]*ModuleOntology{}
	}
	r.ontologies[ref.Namespace] = ontology
	return ontology, nil
}

// RunTask invokes the module's run command with taskInstance supplied through
// the environment variable the module's ontology declares, and returns the
// newline delimited JSON the task wrote to stdout.
//
// Whatever the task wrote is returned even when the container fails, because a
// task reports its own failures as salmodule:Error nodes on stdout before
// exiting non-zero; those messages describe the failure far better than the
// container's exit status does.
func (r *Resolver) RunTask(ctx context.Context, ref ModuleRef, envVar string, taskInstance string) ([]byte, error) {
	return r.runModuleCommand(ctx, ref, []string{envVar + "=" + taskInstance}, RunCommand)
}

func (r *Resolver) runModuleCommand(ctx context.Context, ref ModuleRef, env []string, subcommand string) ([]byte, error) {
	image, err := r.image(ctx, ref)
	if err != nil {
		return nil, err
	}

	runner, err := r.containerRunner()
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := runner.RunContainer(ctx, image, env, []string{BaseCommand, subcommand})
	if len(stderr) > 0 {
		slog.Warn(ref.Namespace + " wrote to stderr: " + strings.TrimSpace(string(stderr)))
	}
	if err != nil {
		return stdout, fmt.Errorf("run %s %s for %s: %w", BaseCommand, subcommand, ref.Namespace, err)
	}
	return stdout, nil
}

// image clones and builds the module the first time it is referenced and
// returns the local image tag it was built as. A module whose image already
// exists on the docker daemon under the tag of the commit the project pins is
// reused without cloning or building anything.
func (r *Resolver) image(ctx context.Context, ref ModuleRef) (string, error) {
	r.mu.Lock()
	image, ok := r.images[ref.Namespace]
	pinnedCommit := r.pinnedCommits[ref.Namespace]
	r.mu.Unlock()
	if ok {
		return image, nil
	}

	runner, err := r.containerRunner()
	if err != nil {
		return "", err
	}

	// the commit pinned in .sal/config.jsonld names the exact image a previous
	// invocation tagged, so finding it on the daemon makes a clone pointless
	if pinnedCommit != "" {
		tag := ref.ImageTagFor(pinnedCommit)
		exists, err := runner.ImageExists(ctx, tag)
		if err != nil {
			return "", err
		}
		if exists {
			slog.Debug("Cache hit for SAL module " + ref.Namespace + ": reusing prebuilt image " + tag + " instead of cloning and building")
			r.remember(ref.Namespace, tag, pinnedCommit)
			return tag, nil
		}
	}

	repoDir, err := os.MkdirTemp("", "sal-module-")
	if err != nil {
		return "", fmt.Errorf("create clone directory for %s: %w", ref.Namespace, err)
	}
	defer func() {
		if err := os.RemoveAll(repoDir); err != nil {
			slog.Warn("failed to clean up module clone " + repoDir + ": " + err.Error())
		}
	}()

	slog.Info("Cloning SAL module " + ref.CloneURL)
	command := r.Command
	if command == nil {
		command = runCommand
	}
	if _, err := command(ctx, "", "git", "clone", "--depth", "1", ref.CloneURL, repoDir); err != nil {
		return "", fmt.Errorf("clone SAL module %s: %w", ref.Namespace, err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "Dockerfile")); err != nil {
		return "", fmt.Errorf("SAL module %s has no Dockerfile in its repository root", ref.Namespace)
	}
	commitOut, err := command(ctx, repoDir, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read HEAD commit of SAL module %s: %w", ref.Namespace, err)
	}
	commit := strings.TrimSpace(string(commitOut))

	// the clone's HEAD may already have an image from an earlier invocation even
	// when nothing pinned it, in which case only the docker build is skipped
	tag := ref.ImageTagFor(commit)
	exists, err := runner.ImageExists(ctx, tag)
	if err != nil {
		return "", err
	}
	if exists {
		slog.Debug("Cache hit for SAL module " + ref.Namespace + ": reusing prebuilt image " + tag + " instead of building")
	} else {
		slog.Info("Building SAL module image " + tag)
		if err := runner.BuildImage(ctx, repoDir, tag); err != nil {
			return "", err
		}
	}

	r.remember(ref.Namespace, tag, commit)
	return tag, nil
}

// remember records the image a module resolved to and the commit it was built
// from, so later references neither clone nor inspect anything.
func (r *Resolver) remember(namespace string, image string, commit string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.images == nil {
		r.images = map[string]string{}
	}
	if r.commits == nil {
		r.commits = map[string]string{}
	}
	r.images[namespace] = image
	r.commits[namespace] = commit
}

// CommitHash returns the git commit hash of the HEAD of the module repository
// the last time it was cloned, cloning and building it first if it has not
// been referenced yet. A salmodule:// vocabulary is pinned by this rather than
// by the digest of its ontology document, since code in the module that
// changes what a task does is not necessarily a change to the ontology itself.
func (r *Resolver) CommitHash(ctx context.Context, ref ModuleRef) (string, error) {
	if _, err := r.image(ctx, ref); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commits[ref.Namespace], nil
}

func (r *Resolver) containerRunner() (ContainerRunner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Runner == nil {
		runner, err := newDockerRunner()
		if err != nil {
			return nil, err
		}
		r.Runner = runner
	}
	return r.Runner, nil
}

// FetchOntologyDocument dereferences a salmodule:// IRI to the module's
// ontology document so that RDF validation can resolve the module's terms, and
// to the git commit hash of the module repository it was built from, which is
// what a salmodule:// vocabulary is pinned at.
func FetchOntologyDocument(iri string) (document []byte, mediaType string, commitHash string, err error) {
	ref, err := ParseModuleIRI(iri)
	if err != nil {
		return nil, "", "", err
	}
	ontology, err := Default().Ontology(context.Background(), ref)
	if err != nil {
		return nil, "", "", err
	}
	commitHash, err = Default().CommitHash(context.Background(), ref)
	if err != nil {
		return nil, "", "", err
	}
	return ontology.Document, "application/ld+json", commitHash, nil
}
