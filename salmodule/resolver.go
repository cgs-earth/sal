package salmodule

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// CommandRunner runs an external command, allowing git usage to be faked in tests.
type CommandRunner func(ctx context.Context, dir string, name string, args ...string) error

func runCommand(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %s: %w", name, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
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
	ontologies map[string]*ModuleOntology
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
	r.ontologies = nil
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
	if err != nil {
		return nil, fmt.Errorf("run %s %s for %s: %w", BaseCommand, subcommand, ref.Namespace, err)
	}
	if len(stderr) > 0 {
		slog.Warn(ref.Namespace + " wrote to stderr: " + strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

// image clones and builds the module the first time it is referenced and
// returns the local image tag it was built as.
func (r *Resolver) image(ctx context.Context, ref ModuleRef) (string, error) {
	r.mu.Lock()
	image, ok := r.images[ref.Namespace]
	r.mu.Unlock()
	if ok {
		return image, nil
	}

	runner, err := r.containerRunner()
	if err != nil {
		return "", err
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
	if err := command(ctx, "", "git", "clone", "--depth", "1", ref.CloneURL, repoDir); err != nil {
		return "", fmt.Errorf("clone SAL module %s: %w", ref.Namespace, err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "Dockerfile")); err != nil {
		return "", fmt.Errorf("SAL module %s has no Dockerfile in its repository root", ref.Namespace)
	}

	slog.Info("Building SAL module image " + ref.ImageTag)
	if err := runner.BuildImage(ctx, repoDir, ref.ImageTag); err != nil {
		return "", err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.images == nil {
		r.images = map[string]string{}
	}
	r.images[ref.Namespace] = ref.ImageTag
	return ref.ImageTag, nil
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
// ontology document so that RDF validation can resolve the module's terms.
func FetchOntologyDocument(iri string) ([]byte, string, error) {
	ref, err := ParseModuleIRI(iri)
	if err != nil {
		return nil, "", err
	}
	ontology, err := Default().Ontology(context.Background(), ref)
	if err != nil {
		return nil, "", err
	}
	return ontology.Document, "application/ld+json", nil
}
