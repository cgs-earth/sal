package salmodule

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/cgs-earth/sal/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// CommandRunner runs an external command and returns its combined output,
// allowing git usage to be faked in tests.
type CommandRunner func(ctx context.Context, dir string, name string, args ...string) ([]byte, error)

func runCommand(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// git asks for credentials on the terminal, or through the askpass helper
	// an IDE's terminal installs, when a repository is private or missing;
	// with its output captured here that looks like a build that hangs, so
	// the prompts are disabled and the failure reported instead. A
	// credential helper that already holds a token is still consulted.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
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
func (r *Resolver) Ontology(ctx context.Context, ref ModuleRef) (_ *ModuleOntology, err error) {
	r.mu.Lock()
	cached, ok := r.ontologies[ref.Namespace]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}
	ctx, span := telemetry.Start(ctx, "salmodule.ontology", attribute.String("sal.module", ref.Namespace))
	defer func() { telemetry.End(span, err) }()

	var stdout []byte
	err = r.runModuleCommand(ctx, ref, nil, OntologyCommand, func(_ context.Context, output ContainerOutput) error {
		// nothing on stderr is part of the ontology, so it is read only to
		// be shown, and read alongside stdout so neither holds up the other
		stderr := make(chan []byte, 1)
		go func() {
			text, _ := io.ReadAll(output.Stderr)
			stderr <- text
		}()
		var err error
		stdout, err = io.ReadAll(output.Stdout)
		if text := strings.TrimSpace(string(<-stderr)); text != "" {
			slog.Warn(ref.Namespace + " wrote to stderr: " + text)
		}
		return err
	})
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

// TaskResult is what running a module task produced: how much newline
// delimited JSON it wrote to stdout, which went to the sink line by line, and
// the files and directories it named with file:/// IRIs, copied out of the
// container into the blob store.
type TaskResult struct {
	OutputBytes int64
	Files       []CopiedFile
}

// TaskStream is what RunTask does with one of a task's output streams: each
// node written on it is checked by Validator, when there is one, and then
// handed to Sink, when there is one. A line is only ever one node, so neither
// sees a line that is not a JSON object; on stdout that is an error, on
// stderr it is a log line sal shows as a warning.
type TaskStream struct {
	Validator *StreamValidator
	Sink      TaskOutputSink
}

// streamLine is one line read from one of a task's output streams, or the
// end of the stream (io.EOF) or the error reading it.
type streamLine struct {
	stderr bool
	line   []byte
	err    error
}

// RunTask invokes the module's run command with taskInstance supplied through
// the TaskInstanceEnvVar environment variable. Each line of the
// newline delimited JSON the task writes to stdout is handed to stdout's sink
// as it is read, so the output is never held whole, and the files the lines
// name with file:/// IRIs are copied out of the container into blobDir as the
// output arrives: a file named by its SHA-256 digest, a directory (a path
// ending in a slash) verbatim under its own path. The container is kept until
// every copy has finished.
//
// Stderr is the task's control channel rather than its data: a line on it
// that is a JSON object is a node, a salmodule:Error reporting why the task
// failed say, and goes to stderr's sink the same way; any other line is a log
// line, shown as a warning and otherwise ignored. Every line the task wrote
// reaches its sink even when the container fails, because a task reports its
// own failures as salmodule:Error nodes before exiting non-zero; those
// messages describe the failure far better than the container's exit status
// does.
//
// A stream's validator checks each node as it arrives, before any file the
// line names is copied or the sink sees it. The first node on either stream
// that fails, or that a sink refuses, ends the run: the container is stopped,
// copies still in flight are abandoned, and the error is returned in place of
// a result.
func (r *Resolver) RunTask(ctx context.Context, ref ModuleRef, taskInstance string, blobDir string, stdout TaskStream, stderr TaskStream) (result TaskResult, err error) {
	ctx, span := telemetry.Start(ctx, "salmodule.run_task", attribute.String("sal.module", ref.Namespace))
	defer func() {
		span.SetAttributes(attribute.Int64("sal.module.output_bytes", result.OutputBytes), attribute.Int("sal.module.files_copied", len(result.Files)))
		telemetry.End(span, err)
	}()
	err = r.runModuleCommand(ctx, ref, []string{TaskInstanceEnvVar + "=" + taskInstance}, RunCommand, func(ctx context.Context, output ContainerOutput) error {
		// copies run under their own context so that a rejected line can
		// abandon them rather than wait for a file the container is still
		// writing
		copyCtx, cancelCopies := context.WithCancel(ctx)
		defer cancelCopies()
		copier := &fileCopier{dir: blobDir, source: strings.TrimSuffix(ref.Namespace, "/"), files: output.Files}
		reject := func(err error) error {
			cancelCopies()
			_, _ = copier.wait()
			return err
		}

		// both streams are read at once, since the container writes them
		// interleaved and one left unread would hold up the other, but every
		// node is handled here on one goroutine, in the order the lines
		// arrived, so the validators and sinks are never called concurrently.
		// Returning early closes the streams, which ends the readers.
		lines := make(chan streamLine)
		done := make(chan struct{})
		defer close(done)
		go readStreamLines(output.Stdout, false, lines, done)
		go readStreamLines(output.Stderr, true, lines, done)
		var stdoutLines, stderrLines int
		for open := 2; open > 0; {
			item := <-lines
			if errors.Is(item.err, io.EOF) {
				open--
				continue
			}
			if item.err != nil {
				return reject(fmt.Errorf("read output of %s: %w", ref.Namespace, item.err))
			}
			if item.stderr {
				stderrLines++
				if err := handleStderrLine(ref.Namespace, stderr, item.line, stderrLines); err != nil {
					return reject(err)
				}
				continue
			}
			stdoutLines++
			result.OutputBytes += int64(len(item.line))
			trimmed := bytes.TrimSpace(item.line)
			if len(trimmed) == 0 {
				continue
			}
			if stdout.Validator != nil {
				if err := stdout.Validator.ValidateLine(trimmed, stdoutLines); err != nil {
					return reject(err)
				}
			}
			copier.observe(copyCtx, trimmed)
			if stdout.Sink != nil {
				if err := stdout.Sink(trimmed, stdoutLines); err != nil {
					return reject(err)
				}
			}
		}

		copied, err := copier.wait()
		if err != nil {
			return fmt.Errorf("copy files from %s: %w", ref.Namespace, err)
		}
		result.Files = copied
		return nil
	})
	return result, err
}

// readStreamLines sends each line of reader, newline included, on lines and
// then io.EOF, or the error that ended the read. It stops once done is closed,
// which is how a consumer that returned early is not blocked on by a reader
// still holding a line.
func readStreamLines(reader io.Reader, stderr bool, lines chan<- streamLine, done <-chan struct{}) {
	buffered := bufio.NewReader(reader)
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			select {
			case lines <- streamLine{stderr: stderr, line: line}:
			case <-done:
				return
			}
		}
		if err != nil {
			select {
			case lines <- streamLine{stderr: stderr, err: err}:
			case <-done:
			}
			return
		}
	}
}

// handleStderrLine deals with one line a task wrote to stderr: a JSON object
// is a node of the task's control channel and goes through the stream's
// validator and sink, anything else is a log line shown as a warning. A line
// that only starts like an object, a Python dict printed for debugging say,
// is a log line too, so that stray logging never fails a task.
func handleStderrLine(namespace string, stderr TaskStream, line []byte, lineNumber int) error {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		slog.Warn(namespace + " wrote to stderr: " + string(trimmed))
		return nil
	}
	if stderr.Validator != nil {
		if err := stderr.Validator.ValidateLine(trimmed, lineNumber); err != nil {
			return err
		}
	}
	if stderr.Sink != nil {
		return stderr.Sink(trimmed, lineNumber)
	}
	return nil
}

func (r *Resolver) runModuleCommand(ctx context.Context, ref ModuleRef, env []string, subcommand string, consume ContainerOutputConsumer) error {
	image, err := r.image(ctx, ref)
	if err != nil {
		return err
	}

	runner, err := r.containerRunner()
	if err != nil {
		return err
	}
	if err := runner.RunContainer(ctx, image, env, []string{BaseCommand, subcommand}, consume); err != nil {
		return fmt.Errorf("run %s %s for %s: %w", BaseCommand, subcommand, ref.Namespace, err)
	}
	return nil
}

// image clones and builds the module the first time it is referenced and
// returns the local image tag it was built as. A module whose image already
// exists on the docker daemon under the tag of the commit the project pins is
// reused without cloning or building anything.
func (r *Resolver) image(ctx context.Context, ref ModuleRef) (_ string, err error) {
	r.mu.Lock()
	image, ok := r.images[ref.Namespace]
	pinnedCommit := r.pinnedCommits[ref.Namespace]
	r.mu.Unlock()
	if ok {
		return image, nil
	}
	// the clone and the docker build are the slow steps of a build that
	// references a module, so each is a span under the resolution itself
	ctx, span := telemetry.Start(ctx, "salmodule.resolve", attribute.String("sal.module", ref.Namespace))
	defer func() { telemetry.End(span, err) }()

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
			span.SetAttributes(attribute.String("sal.module.commit", pinnedCommit), attribute.String("sal.module.image", tag), attribute.String("sal.module.cache", "pinned image"))
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
	cloneCtx, cloneSpan := telemetry.Start(ctx, "salmodule.clone", attribute.String("sal.module.clone_url", ref.CloneURL))
	_, err = command(cloneCtx, "", "git", "clone", "--depth", "1", ref.CloneURL, repoDir)
	telemetry.End(cloneSpan, err)
	if err != nil {
		if strings.Contains(err.Error(), "could not read Username") || strings.Contains(err.Error(), "Authentication failed") {
			return "", fmt.Errorf("clone SAL module %s: %w\n%s is private or does not exist; sal clones modules without prompting for credentials, so make the repository public or store a token in a git credential helper (for example with `gh auth setup-git`)", ref.Namespace, err, ref.CloneURL)
		}
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
	span.SetAttributes(attribute.String("sal.module.commit", commit), attribute.String("sal.module.image", tag))
	if exists {
		slog.Debug("Cache hit for SAL module " + ref.Namespace + ": reusing prebuilt image " + tag + " instead of building")
		span.SetAttributes(attribute.String("sal.module.cache", "image"))
	} else {
		slog.Info("Building SAL module image " + tag)
		span.SetAttributes(attribute.String("sal.module.cache", "none"))
		buildCtx, buildSpan := telemetry.Start(ctx, "salmodule.build_image", attribute.String("sal.module.image", tag))
		err = runner.BuildImage(buildCtx, repoDir, tag)
		telemetry.End(buildSpan, err)
		if err != nil {
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
func FetchOntologyDocument(ctx context.Context, iri string) (document []byte, mediaType string, commitHash string, err error) {
	ref, err := ParseModuleIRI(iri)
	if err != nil {
		return nil, "", "", err
	}
	ontology, err := Default().Ontology(ctx, ref)
	if err != nil {
		return nil, "", "", err
	}
	commitHash, err = Default().CommitHash(ctx, ref)
	if err != nil {
		return nil, "", "", err
	}
	return ontology.Document, "application/ld+json", commitHash, nil
}
