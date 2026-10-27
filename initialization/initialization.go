package initialization

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	_ "embed"

	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/cgs-earth/sal/specs"
)

//go:embed sal_config_example.jsonld
var salConfigTemplate string

type InitCmd struct {
	Description string `arg:"--description" help:"a sentence or two describing the project, recorded in .sal/config.jsonld; asked for when not given"`
	SalModule   bool   `arg:"--salmodule" help:"also scaffold the files a SAL module needs: ontology.jsonld, Dockerfile, and .dockerignore"`
	Bare        bool   `arg:"--bare" help:"with --salmodule, answer no questions and leave the task class blank"`
	UI          bool   `arg:"--ui" help:"with --salmodule, open the browser form that writes ontology.jsonld even where sal would not on its own (CI, an SSH session, no terminal or display)"`
	NoUI        bool   `arg:"--no-ui" help:"with --salmodule, answer the questions at the prompt instead of opening the browser form"`
}

// ErrUIWithoutSalModule is returned when --ui or --no-ui is passed without
// --salmodule, since the form only writes a module's ontology.
var ErrUIWithoutSalModule = errors.New("--ui and --no-ui only apply with --salmodule; the form writes a SAL module's ontology.jsonld")

// ErrDescriptionRequired is returned when neither --description nor the
// prompt gave a description, since a project is not initialized without one.
var ErrDescriptionRequired = errors.New("a description of the project is required; answer the prompt or pass --description")

// stdin is where the prompts read their answers from; tests replace it.
var stdin io.Reader = os.Stdin

// taskIDPattern is what a task ID typed at the prompt must match: a name
// usable as a relative IRI and a JSON-LD @type, with no spaces or punctuation.
var taskIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// taskDetails is what the sample task prompts collect. A zero value leaves the
// task class in ontology.jsonld blank.
type taskDetails struct {
	ID      string
	Label   string
	Comment string
}

func (cmd *InitCmd) Run() error {
	if (cmd.UI || cmd.NoUI) && !cmd.SalModule {
		return ErrUIWithoutSalModule
	}

	// if cwd is home return an error
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if cwd == home {
		return pkg.ErrCantMakeSalDirInHome
	}

	gitCmd := exec.Command("git", "remote", "-v")
	out, err := gitCmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError

		switch {
		case errors.Is(err, exec.ErrNotFound):
			return fmt.Errorf("git is not installed or not in PATH; you must have git installed for SAL")

		case errors.As(err, &exitErr):
			log := string(out)
			if strings.Contains(log, "not a git repository") {
				return fmt.Errorf("current directory is not a git repository; SAL must be ran inside a git repository")
			}

			return fmt.Errorf("git command failed: %s", strings.TrimSpace(log))

		default:
			return fmt.Errorf("failed to execute git: %w", err)
		}
	}

	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("git repository has no remotes configured; you must specify a remote before running init")
	}

	// DefaultGitRemote maps an ssh remote to https and errors when it cannot;
	// anything else left over is a scheme SAL does not support as a base URL
	remote, err := pkg.DefaultGitRemote()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(remote, "https://") && !strings.HasPrefix(remote, "http://") {
		return fmt.Errorf("git remote %q uses an unsupported scheme; SAL requires an https remote or an ssh remote that can be mapped to https", remote)
	}

	cwd, err = os.Getwd()
	if err != nil {
		return err
	}

	// A SAL module is a container that produces data for projects, not a
	// project itself, so the project layout is only made without --salmodule:
	// .sal/data for what sal builds, sparql/ for the project's SPARQL queries
	// (source, so it sits in the project root rather than under the gitignored
	// .sal/data), the .gitignore entries for what sal generates, and the
	// project's own node in .sal/config.jsonld carrying its description. A
	// project that already has the file is initialized, and init refuses to
	// write over what it records; the description is asked for before anything
	// is written so that giving up at the prompt leaves nothing behind.
	if !cmd.SalModule {
		configPath := filepath.Join(cwd, ".sal", "config.jsonld")
		if _, err := os.Stat(configPath); err == nil {
			return fmt.Errorf("%s already exists; this directory is already a SAL project", configPath)
		} else if !os.IsNotExist(err) {
			return err
		}
		description := cmd.Description
		if description == "" {
			if description, err = promptDescription(stdin); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Join(cwd, ".sal", "data"), 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(cwd, "sparql"), 0755); err != nil {
			return err
		}
		if err := ignoreGeneratedDirectories(cwd); err != nil {
			return err
		}
		base, err := pkg.DefaultSalBase()
		if err != nil {
			return err
		}
		title, err := pkg.GitProjectName()
		if err != nil {
			return err
		}
		// the project's own node, "@id": "." typed schema:Project and titled
		// after the repository, which build validates and commits like a source
		// and sal import adds its imports to
		if err := importation.WriteOntology(configPath, &importation.Ontology{IRI: base, Base: base, IsProject: true, Title: title, Description: description}); err != nil {
			return err
		}
	}

	home, err = os.UserHomeDir()
	if err != nil {
		return err
	}
	salCacheDir := filepath.Join(home, ".sal", "cache")

	err = os.MkdirAll(salCacheDir, 0755)
	if err != nil {
		return err
	}

	err = os.WriteFile(filepath.Join(home, ".sal", "config.jsonld"), []byte(salConfigTemplate), 0644)
	if err != nil {
		return err
	}

	// the form writes ontology.jsonld and the files next to it in place of
	// the prompts
	if cmd.SalModule && cmd.opensForm() {
		return serveModuleInitUI(cwd)
	}

	if cmd.SalModule {
		var task taskDetails
		if !cmd.Bare {
			if task, err = promptTaskDetails(stdin); err != nil {
				return err
			}
		}
		ontology, err := salModuleOntology(task)
		if err != nil {
			return err
		}
		if err := writeSalModuleFiles(cwd, ontology); err != nil {
			return err
		}
		if err := writeEditorSchema(cwd); err != nil {
			return err
		}
		slog.Info("SAL module initialized at " + cwd)
		return nil
	}

	slog.Info("SAL project initialized at " + cwd)
	return nil
}

// ignoreGeneratedDirectories adds .sal/data and .sal/constructed, which hold
// what sal generates, to the project's .gitignore, leaving an entry the file
// already has as it is.
func ignoreGeneratedDirectories(dir string) error {
	gitignorePath := filepath.Join(dir, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	ignored := string(content)
	for _, entry := range []string{".sal/data", ".sal/constructed"} {
		if strings.Contains(ignored, entry) {
			continue
		}
		if ignored != "" && !strings.HasSuffix(ignored, "\n") {
			ignored += "\n"
		}
		ignored += entry + "\n"
	}
	if ignored == string(content) {
		return nil
	}
	return os.WriteFile(gitignorePath, []byte(ignored), 0644)
}

// opensForm decides whether `sal init --salmodule` writes ontology.jsonld
// from the browser form, which is the default, or from the prompts. --ui and
// --no-ui decide outright; --bare asks nothing, so it takes the prompts; and
// otherwise the form is skipped, saying why, where no browser is likely to
// open.
func (cmd *InitCmd) opensForm() bool {
	switch {
	case cmd.UI:
		return true
	case cmd.NoUI || cmd.Bare:
		return false
	}
	if reason := headlessReason(); reason != "" {
		slog.Info("not opening the browser form since " + reason + "; asking at the prompt instead (pass --ui to open it anyway)")
		return false
	}
	return true
}

// headlessReason reports why no browser is likely to open for this session,
// or "" when one is: a CI environment, an SSH session, standard output that
// is not a terminal, or a Linux or BSD session with no display. Tests replace
// it.
var headlessReason = func() string {
	if os.Getenv("CI") != "" {
		return "CI is set"
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return "this is an SSH session"
	}
	if info, err := os.Stdout.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "standard output is not a terminal"
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return "there is no display"
	}
	return ""
}

// askLine prints a question and reads the answer, trimmed, from reader. The
// bool is false once the input has run out, as happens when stdin is not a
// terminal, so that a prompt can fall back to a default rather than wait.
func askLine(reader *bufio.Reader, question string) (string, bool, error) {
	fmt.Print(question + ": ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	line = strings.TrimSpace(line)
	if line == "" && errors.Is(err, io.EOF) {
		fmt.Println()
		return "", false, nil
	}
	return line, true, nil
}

// promptDescription asks for the project's description, which is required:
// an empty answer is asked again, and running out of input, as happens when
// stdin is not a terminal, is ErrDescriptionRequired.
func promptDescription(in io.Reader) (string, error) {
	reader := bufio.NewReader(in)
	for {
		description, ok, err := askLine(reader, "Description of the project")
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrDescriptionRequired
		}
		if description != "" {
			return description, nil
		}
	}
}

// promptTaskDetails asks the user for a sample task to seed ontology.jsonld
// with: an ID, a human readable name, and a comment about what it does. The
// ID is asked again until it has no spaces or special characters. Running out
// of input, as happens when stdin is not a terminal, leaves the task blank,
// the same as --bare.
func promptTaskDetails(in io.Reader) (taskDetails, error) {
	fmt.Println("A SAL module declares the tasks a project can run as classes in ontology.jsonld.")
	fmt.Println("sal will ask a few questions about one task so that the ontology starts with a")
	fmt.Println("worked example to extend rather than blank fields. Pass --bare to skip this.")
	fmt.Println()

	reader := bufio.NewReader(in)
	var task taskDetails
	for task.ID == "" {
		id, ok, err := askLine(reader, "Unique ID for the task, with no spaces or special characters (e.g. FetchStations)")
		if err != nil {
			return taskDetails{}, err
		}
		if !ok {
			slog.Warn("no input available for the sample task; leaving the task class blank as --bare would")
			return taskDetails{}, nil
		}
		if !taskIDPattern.MatchString(id) {
			slog.Warn("a task ID must start with a letter and contain only letters, digits, and underscores", "id", id)
			continue
		}
		task.ID = id
	}

	label, ok, err := askLine(reader, "Human readable name for the task (e.g. Fetch weather stations)")
	if err != nil {
		return taskDetails{}, err
	}
	task.Label = label
	if !ok {
		return task, nil
	}

	comment, _, err := askLine(reader, "Comment describing what the task does")
	if err != nil {
		return taskDetails{}, err
	}
	task.Comment = comment
	return task, nil
}

// salModuleOntology renders the ontology.jsonld skeleton a new SAL module
// starts from: a @context binding every prefix the document uses and a @graph
// holding the owl:Ontology node, named "." like a project ontology, titled
// (dcterms:title) after the git repository and credited (dcterms:creator) to
// the configured git user, followed
// by one owl:Class that is a salmodule:Task. The task class carries whatever
// promptTaskDetails collected, and when it collected an ID the class's
// salmodule:taskShape becomes a SHACL node shape targeting it; otherwise every
// string is blank and the shape is an empty object for the author to fill in.
func salModuleOntology(task taskDetails) (string, error) {
	title, err := pkg.GitProjectName()
	if err != nil {
		return "", err
	}
	creator, err := pkg.GitUserName()
	if err != nil {
		return "", err
	}
	if creator == "" {
		slog.Warn("git user.name is not set; leaving dcterms:creator blank in ontology.jsonld")
	}
	type iri struct {
		ID string `json:"@id"`
	}
	type ontologyNode struct {
		ID      string `json:"@id"`
		Type    string `json:"@type"`
		Creator string `json:"dcterms:creator"`
		Title   string `json:"dcterms:title"`
		Version string `json:"owl:versionInfo"`
		Label   string `json:"rdfs:label"`
		Comment string `json:"rdfs:comment"`
	}
	type nodeShape struct {
		Type        string `json:"@type"`
		TargetClass iri    `json:"sh:targetClass"`
		Property    []any  `json:"sh:property"`
	}
	type taskNode struct {
		ID         string `json:"@id"`
		Type       string `json:"@type"`
		SubClassOf iri    `json:"rdfs:subClassOf"`
		Label      string `json:"rdfs:label"`
		Comment    string `json:"rdfs:comment"`
		TaskShape  any    `json:"salmodule:taskShape"`
	}
	context := map[string]string{
		"dcterms":   "http://purl.org/dc/terms/",
		"owl":       "http://www.w3.org/2002/07/owl#",
		"rdfs":      "http://www.w3.org/2000/01/rdf-schema#",
		"salmodule": salmodule.Namespace,
	}
	var shape any = struct{}{}
	if task.ID != "" {
		context["sh"] = "http://www.w3.org/ns/shacl#"
		shape = nodeShape{Type: "sh:NodeShape", TargetClass: iri{ID: task.ID}, Property: []any{}}
	}
	doc := struct {
		Context map[string]string `json:"@context"`
		Graph   []any             `json:"@graph"`
	}{
		Context: context,
		Graph: []any{
			ontologyNode{ID: ".", Type: "owl:Ontology", Creator: creator, Title: title, Version: ""},
			taskNode{
				ID:         task.ID,
				Type:       "owl:Class",
				SubClassOf: iri{ID: "salmodule:Task"},
				Label:      task.Label,
				Comment:    task.Comment,
				TaskShape:  shape,
			},
		},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// writeSalModuleFiles scaffolds the files a SAL module repository needs on top
// of a SAL project: the ontology.jsonld skeleton plus a Dockerfile and
// .dockerignore holding only a comment saying what belongs in each. A file that already exists is left alone so that re-running
// init never clobbers a module's own work.
func writeSalModuleFiles(dir string, ontology string) error {
	files := []struct{ name, content string }{
		{"ontology.jsonld", ontology},
		{"Dockerfile", "# When invoked this Dockerfile should stream JSON-LD to standard out in accordance with the salmodule specification.\n"},
		{".dockerignore", "# Ignore any files that should not be packaged into the salmodule container ran by the SAL cli.\n"},
	}
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if _, err := os.Stat(path); err == nil {
			slog.Warn(file.name + " already exists; leaving it unchanged")
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(path, []byte(file.content), 0644); err != nil {
			return err
		}
		slog.Info("created " + file.name)
	}
	return nil
}

// editorSchemaURL is where .vscode/settings.json points VS Code for the
// schema, relative to the workspace root the settings file sits under.
const editorSchemaURL = "./.vscode/" + specs.SalModuleOntologySchemaFile

// writeEditorSchema copies the SAL module ontology JSON Schema into .vscode
// and registers it in .vscode/settings.json for ontology.jsonld, so that an
// editor checks the ontology against the SAL Module specification as it is
// written. The schema copy is sal's, not the module's, so a stale copy is
// rewritten; the settings file is the module's, so an existing one gains
// only the json.schemas entry, and one sal cannot parse (settings.json may
// hold comments) is left alone with a warning saying what to add by hand.
func writeEditorSchema(dir string) error {
	vscodeDir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0755); err != nil {
		return err
	}
	schemaPath := filepath.Join(vscodeDir, specs.SalModuleOntologySchemaFile)
	if existing, err := os.ReadFile(schemaPath); err != nil && !os.IsNotExist(err) {
		return err
	} else if !bytes.Equal(existing, specs.SalModuleOntologySchema) {
		if err := os.WriteFile(schemaPath, specs.SalModuleOntologySchema, 0644); err != nil {
			return err
		}
		slog.Info("created .vscode/" + specs.SalModuleOntologySchemaFile)
	}

	entry := map[string]any{
		"fileMatch": []any{"/ontology.jsonld"},
		"url":       editorSchemaURL,
	}
	settingsPath := filepath.Join(vscodeDir, "settings.json")
	settings := map[string]any{}
	content, err := os.ReadFile(settingsPath)
	switch {
	case err != nil && !os.IsNotExist(err):
		return err
	case err == nil:
		if strings.Contains(string(content), editorSchemaURL) {
			return nil
		}
		if err := json.Unmarshal(content, &settings); err != nil {
			slog.Warn(".vscode/settings.json could not be parsed as JSON; add the schema to it yourself", "json.schemas", entry)
			return nil
		}
	}
	schemas, _ := settings["json.schemas"].([]any)
	settings["json.schemas"] = append(schemas, entry)
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath, append(out, '\n'), 0644); err != nil {
		return err
	}
	slog.Info("registered the schema for ontology.jsonld in .vscode/settings.json")
	return nil
}
