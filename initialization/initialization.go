package initialization

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	_ "embed"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
)

//go:embed sal_config_example.jsonld
var salConfigTemplate string

type InitCmd struct {
	SalModule bool `arg:"--salmodule" help:"also scaffold the files a SAL module needs: ontology.jsonld, Dockerfile, and .dockerignore"`
	Bare      bool `arg:"--bare" help:"with --salmodule, skip the prompts that fill in a sample task and leave the task class blank"`
}

// stdin is where the sample task prompts read their answers from; tests
// replace it.
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
	salDataDir := filepath.Join(cwd, ".sal", "data")

	err = os.MkdirAll(salDataDir, 0755)
	if err != nil {
		return err
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

	// check if .gitignore is present in the cwd, if not create it and add .sal/data to it
	gitignorePath := filepath.Join(cwd, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		err = os.WriteFile(gitignorePath, []byte(".sal/data\n"), 0644)
		if err != nil {
			return err
		}
	} else if err == nil {
		// check if .sal/data is already in .gitignore
		content, err := os.ReadFile(gitignorePath)
		if err != nil {
			return err
		}
		if !strings.Contains(string(content), ".sal/data") {
			f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if _, err := f.WriteString("\n.sal/data\n"); err != nil {
				return err
			}
		}
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
	}

	slog.Info("SAL project initialized at " + cwd)
	return nil
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
	ask := func(question string) (string, bool, error) {
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

	var task taskDetails
	for task.ID == "" {
		id, ok, err := ask("Unique ID for the task, with no spaces or special characters (e.g. FetchStations)")
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

	label, ok, err := ask("Human readable name for the task (e.g. Fetch weather stations)")
	if err != nil {
		return taskDetails{}, err
	}
	task.Label = label
	if !ok {
		return task, nil
	}

	comment, _, err := ask("Comment describing what the task does")
	if err != nil {
		return taskDetails{}, err
	}
	task.Comment = comment
	return task, nil
}

// salModuleOntology renders the ontology.jsonld skeleton a new SAL module
// starts from: a @context binding every prefix the document uses and a @graph
// holding the owl:Ontology node, named "." like a project ontology, titled
// after the git repository and credited to the configured git user, followed
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
		slog.Warn("git user.name is not set; leaving dc:creator blank in ontology.jsonld")
	}
	type iri struct {
		ID string `json:"@id"`
	}
	type ontologyNode struct {
		ID      string `json:"@id"`
		Type    string `json:"@type"`
		Creator string `json:"dc:creator"`
		Title   string `json:"dc:title"`
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
		"dc":        "http://purl.org/dc/elements/1.1/",
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
