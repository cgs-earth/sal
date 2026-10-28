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

	"github.com/cgs-earth/sal/edit"
	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/cgs-earth/sal/terraform"
)

//go:embed sal_config_example.jsonld
var salConfigTemplate string

type InitCmd struct {
	Description string `arg:"--description" help:"a sentence or two describing the project, recorded in .sal/config.jsonld; asked for when not given"`
	SalModule   bool   `arg:"--salmodule" help:"also scaffold the files a SAL module needs: ontology.jsonld, Dockerfile, and .dockerignore"`
	Bare        bool   `arg:"--bare" help:"with --salmodule, answer no questions and leave the task class blank"`
	UI          bool   `arg:"--ui" help:"with --salmodule, open the browser form that writes ontology.jsonld even where sal would not on its own (CI, an SSH session, no terminal or display)"`
	NoUI        bool   `arg:"--no-ui" help:"with --salmodule, answer the questions at the prompt instead of opening the browser form"`
	WithIaC     bool   `arg:"--with-iac" help:"also write the Terraform configuration sal terraform generates, deploying the data product on Google Cloud, into terraform/"`
	Skills      bool   `arg:"--skills" help:"in a project that is already initialized, only install (or update) the SAL agent skills in .claude/skills and .agents/skills; a new project or module gets them anyway"`
	NoSkills    bool   `arg:"--no-skills" help:"do not install the SAL agent skills"`
}

// ErrIaCWithSalModule is returned when --with-iac is passed with --salmodule,
// since the configuration deploys a data product and a module has none.
var ErrIaCWithSalModule = errors.New("--with-iac only applies to a project; a SAL module has no data product to deploy")

// ErrSkillsFlags is returned when --skills and --no-skills are both passed.
var ErrSkillsFlags = errors.New("--skills and --no-skills cannot both be given")

// ErrUIWithoutSalModule is returned when --ui or --no-ui is passed without
// --salmodule, since the form only writes a module's ontology.
var ErrUIWithoutSalModule = errors.New("--ui and --no-ui only apply with --salmodule; the form writes a SAL module's ontology.jsonld")

// ErrDescriptionRequired is returned when neither --description nor the
// prompt gave a description, since a project is not initialized without one.
var ErrDescriptionRequired = errors.New("a description of the project is required; answer the prompt or pass --description")

// stdin is where the prompts read their answers from; tests replace it.
var stdin io.Reader = os.Stdin

// promptReader buffers stdin for every prompt of an invocation. One reader
// is shared, rather than one per prompt, since a buffered reader may take
// more than the line it was asked for from the source and a second reader
// over the same source would then miss it.
var (
	promptReader *bufio.Reader
	promptSource io.Reader
)

func prompts() *bufio.Reader {
	if promptReader == nil || promptSource != stdin {
		promptSource = stdin
		promptReader = bufio.NewReader(stdin)
	}
	return promptReader
}

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
	if cmd.WithIaC && cmd.SalModule {
		return ErrIaCWithSalModule
	}
	if cmd.Skills && cmd.NoSkills {
		return ErrSkillsFlags
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
			// the one thing init adds to an initialized project is the
			// skills, since nothing else about it can be re-run
			if cmd.Skills {
				return pkg.InstallSkills(cwd, pkg.SalSkill)
			}
			return fmt.Errorf("%s already exists; this directory is already a SAL project", configPath)
		} else if !os.IsNotExist(err) {
			return err
		}
		description := cmd.Description
		if description == "" {
			if description, err = promptDescription(); err != nil {
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
		if err := writeMakefile(cwd); err != nil {
			return err
		}
		if cmd.WithIaC {
			if err := writeTerraform(cwd, title); err != nil {
				return err
			}
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

	// the skill is installed before the form, which runs until the user
	// stops it
	skill := pkg.SalSkill
	if cmd.SalModule {
		skill = pkg.SalModuleSkill
	}
	if err := cmd.installSkills(cwd, skill); err != nil {
		return err
	}

	// the form writes ontology.jsonld and the files next to it in place of
	// the prompts, and edits an ontology.jsonld the module already has
	if cmd.SalModule && cmd.opensForm() {
		form, err := edit.NewForm(cwd, "init")
		if err != nil {
			return err
		}
		form.Written = func() {
			slog.Info("SAL module initialized at " + cwd + "; press Ctrl+C to stop the form")
		}
		return form.Serve(0, cmd.UI)
	}

	if cmd.SalModule {
		var task taskDetails
		if !cmd.Bare {
			if task, err = promptTaskDetails(); err != nil {
				return err
			}
		}
		ontology, err := salModuleOntology(task)
		if err != nil {
			return err
		}
		if err := salmodule.WriteModuleFiles(cwd, ontology, false); err != nil {
			return err
		}
		if err := salmodule.WriteEditorSchema(cwd); err != nil {
			return err
		}
		slog.Info("SAL module initialized at " + cwd)
		return nil
	}

	slog.Info("SAL project initialized at " + cwd)
	return nil
}

// installSkills installs the named agent skill into the project or module,
// which every init does unless --no-skills says not to, since the skill is
// what makes a coding agent useful in the repository. A download that fails
// only warns, since the project is initialized by then and `sal init
// --skills` installs the skill later; with --skills, which asked for nothing
// else, it is the error.
func (cmd *InitCmd) installSkills(dir string, skill string) error {
	if cmd.NoSkills {
		return nil
	}
	err := pkg.InstallSkills(dir, skill)
	if err != nil && !cmd.Skills {
		slog.Warn("could not install the "+skill+" agent skill; run `sal init --skills` to try again, or pass --no-skills", "error", err)
		return nil
	}
	return err
}

// writeTerraform writes the Terraform configuration `sal terraform` would
// for a project named name into the project, leaving a configuration the
// directory already holds alone with a warning rather than failing an init.
func writeTerraform(dir string, name string) error {
	written, err := terraform.Generate(filepath.Join(dir, terraform.DefaultDir), terraform.Options{Name: name})
	if errors.Is(err, terraform.ErrExists) {
		slog.Warn("leaving the Terraform configuration the project already has alone; `sal terraform --force` replaces it", "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	for _, path := range written {
		slog.Info("wrote " + path)
	}
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
	if reason := pkg.HeadlessReason(); reason != "" {
		slog.Info("not opening the browser form since " + reason + "; asking at the prompt instead (pass --ui to open it anyway)")
		return false
	}
	return true
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
func promptDescription() (string, error) {
	reader := prompts()
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
func promptTaskDetails() (taskDetails, error) {
	fmt.Println("A SAL module declares the tasks a project can run as classes in ontology.jsonld.")
	fmt.Println("sal will ask a few questions about one task so that the ontology starts with a")
	fmt.Println("worked example to extend rather than blank fields. Pass --bare to skip this.")
	fmt.Println()

	reader := prompts()
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

// initialModuleVersion is the owl:versionInfo a new module starts at, the
// same the browser form starts from.
const initialModuleVersion = "0.1"

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
			ontologyNode{ID: ".", Type: "owl:Ontology", Creator: creator, Title: title, Version: initialModuleVersion},
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
