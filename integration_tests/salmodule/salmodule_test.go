//go:build integration

package salmoduleintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/build"
	"github.com/cgs-earth/sal/initialization"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// moduleNamespace is the salmodule:// IRI the test project references. SAL
// derives a GitHub clone URL from it, which the suite redirects at the local
// fixture repository so the test never reaches the network for the module.
const moduleNamespace = "salmodule://github.com/cgs-earth/sal-integration-test-module/"

// placeNamespace heads the @id of every place the fixture module emits, so it is
// what tells the module's output apart from the rest of what a build writes.
const placeNamespace = "https://example.test/place/"

// SalModuleSuite exercises SAL's docker round trip against the fixture module in
// ./module: SAL clones it, builds its Dockerfile, runs the SAL Module command
// line interface inside the container, and folds the RDF it emits into a built
// Iceberg table.
type SalModuleSuite struct {
	suite.Suite

	// moduleRepo is a git repository holding the fixture module's source.
	moduleRepo string
	// originalWorkingDir is restored after each test, since SAL discovers the
	// project it operates on by walking up from the working directory.
	originalWorkingDir string
}

func TestSalModuleSuite(t *testing.T) {
	suite.Run(t, new(SalModuleSuite))
}

func (s *SalModuleSuite) SetupSuite() {
	workingDir, err := os.Getwd()
	s.Require().NoError(err)
	s.originalWorkingDir = workingDir

	s.moduleRepo = s.commitFixtureModuleToGitRepository()

	resolver := salmodule.Default()
	resolver.Reset()
	resolver.Command = s.cloneFixtureModule
	s.T().Cleanup(func() {
		resolver.Command = nil
		resolver.Reset()
	})
}

func (s *SalModuleSuite) TearDownTest() {
	s.Require().NoError(os.Chdir(s.originalWorkingDir))
}

// commitFixtureModuleToGitRepository copies ./module into a git repository so
// that SAL's clone step runs against a real repository rather than a directory.
func (s *SalModuleSuite) commitFixtureModuleToGitRepository() string {
	repo := s.T().TempDir()
	for _, name := range []string{"Dockerfile", "salmodule.sh"} {
		content, err := os.ReadFile(filepath.Join("module", name))
		s.Require().NoError(err)
		s.Require().NoError(os.WriteFile(filepath.Join(repo, name), content, 0755))
	}

	s.git(repo, "init")
	s.git(repo, "add", ".")
	s.git(repo, "-c", "user.email=sal@example.test", "-c", "user.name=SAL", "commit", "-m", "fixture module")
	return repo
}

// cloneFixtureModule stands in for SAL's git clone. It performs a real clone but
// substitutes the local fixture repository for the remote URL SAL derived from
// the salmodule:// IRI.
func (s *SalModuleSuite) cloneFixtureModule(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	rewritten := make([]string, len(args))
	copy(rewritten, args)
	for i, arg := range rewritten {
		if strings.HasPrefix(arg, "https://") {
			rewritten[i] = s.moduleRepo
		}
	}

	command := exec.CommandContext(ctx, name, rewritten...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %s: %w", name, strings.Join(rewritten, " "), strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func (s *SalModuleSuite) git(dir string, args ...string) {
	command := exec.Command("git", args...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	s.Require().NoErrorf(err, "git %s: %s", strings.Join(args, " "), string(out))
}

// newSalProject creates an initialized SAL project holding source, and makes it
// the working directory for the rest of the test. Everything sal init wrote is
// committed, so the project starts from the clean worktree sal run requires.
func (s *SalModuleSuite) newSalProject(source string) string {
	project := s.T().TempDir()
	s.git(project, "init")
	s.git(project, "remote", "add", "origin", "https://github.com/cgs-earth/sal-integration-test-project.git")
	s.Require().NoError(os.WriteFile(filepath.Join(project, "module.ttl"), []byte(source), 0644))
	s.git(project, "add", ".")
	s.git(project, "-c", "user.email=sal@example.test", "-c", "user.name=SAL", "commit", "-m", "project source")

	s.Require().NoError(os.Chdir(project))
	s.Require().NoError((&initialization.InitCmd{}).Run())
	s.git(project, "add", "-A")
	s.git(project, "-c", "user.email=sal@example.test", "-c", "user.name=SAL", "commit", "-m", "sal init")
	return project
}

// buildForRun puts a project in the state sal run requires: the first build
// pins the module's vocabulary into .sal/config.jsonld, committing that moves
// HEAD, and the second build tags the table with the commit the worktree now
// sits at.
func (s *SalModuleSuite) buildForRun(project string) {
	_, err := (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg}).Run()
	s.Require().NoError(err)
	s.git(project, "add", "-A")
	s.git(project, "-c", "user.email=sal@example.test", "-c", "user.name=SAL", "commit", "-m", "pin vocabularies")
	_, err = (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg}).Run()
	s.Require().NoError(err)
}

// projectReferencing is the RDF of a SAL project that declares one task
// instance of the fixture module's StaticPlaceProducer class. Each entry of
// configuration is a property of the instance, which is the only way a project
// tells a module how to run: SAL serializes the properties the module's own
// vocabulary defines into the task instance the container is invoked with.
func projectReferencing(configuration ...string) string {
	instance := "<Places> a fixture:StaticPlaceProducer"
	for _, property := range configuration {
		instance += " ;\n    " + property
	}

	return fmt.Sprintf(`
@prefix fixture: <%s> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

%s .
`, moduleNamespace, instance)
}

// TestModuleImplementsSalModuleCli runs the fixture module in an ephemeral
// container of its own, independently of SAL, to confirm the fixture really
// does implement the command line interface SAL expects of a module.
func (s *SalModuleSuite) TestModuleImplementsSalModuleCli() {
	ctx := context.Background()
	container, err := testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context:    "module",
			Repo:       "sal-integration-test-module",
			Tag:        "cli-check",
			KeepImage:  true,
			Dockerfile: "Dockerfile",
		}),
		testcontainers.WithCmd(salmodule.BaseCommand, salmodule.OntologyCommand),
		testcontainers.WithWaitStrategy(wait.ForExit().WithExitTimeout(time.Minute)),
	)
	testcontainers.CleanupContainer(s.T(), container)
	s.Require().NoError(err)

	logs, err := container.Logs(ctx)
	s.Require().NoError(err)
	defer func() { _ = logs.Close() }()

	var ontology struct {
		Context map[string]string `json:"@context"`
		Graph   []map[string]any  `json:"@graph"`
	}
	s.Require().NoError(json.NewDecoder(logs).Decode(&ontology))
	s.Equal(salmodule.Namespace, ontology.Context["salmodule"])
	s.NotEmpty(ontology.Graph)
}

// TestRunMaterializesModuleTriples is the round trip: a project referencing
// the module is built, which commits the task's configuration without running
// it, and sal run then invokes the module and commits the triples it produced,
// which are read back out of the Iceberg table SAL wrote.
func (s *SalModuleSuite) TestRunMaterializesModuleTriples() {
	project := s.newSalProject(projectReferencing())
	s.buildForRun(project)

	// the builds committed the configuration but ran nothing
	s.Empty(s.builtObjectsForPredicate("https://schema.org/name"))

	graph, err := (&build.RunCmd{Paths: []string{"module.ttl"}}).Run()

	s.Require().NoError(err)
	s.Require().NotNil(graph)

	objects := s.builtObjectsForPredicate("https://schema.org/name")
	s.Contains(objects, "Lake Tahoe")
	s.Contains(objects, "Lake Erie")
}

// TestRunConfiguresTheTaskWithItsRDFProperties is what makes the module's
// configuration RDF rather than an embedded JSON-LD literal: fixture:region is a
// property of the instance, and the places the run ends up with are only the
// ones the module emits for the region it was given.
func (s *SalModuleSuite) TestRunConfiguresTheTaskWithItsRDFProperties() {
	project := s.newSalProject(projectReferencing(`fixture:region "west"`))
	s.buildForRun(project)

	graph, err := (&build.RunCmd{Paths: []string{"module.ttl"}}).Run()

	s.Require().NoError(err)
	s.Require().NotNil(graph)

	objects := s.builtObjectsForPredicate("https://schema.org/name")
	s.Contains(objects, "Lake Tahoe")
	s.NotContains(objects, "Lake Erie")
}

// TestRunConfiguresTheTaskWithTypedLiterals checks the same path for a
// property whose literal carries a datatype, which reaches the module as a
// JSON-LD value object rather than as a bare JSON value.
func (s *SalModuleSuite) TestRunConfiguresTheTaskWithTypedLiterals() {
	project := s.newSalProject(projectReferencing(`fixture:region "east"`, `fixture:labelled "true"^^xsd:boolean`))
	s.buildForRun(project)

	graph, err := (&build.RunCmd{Paths: []string{"module.ttl"}}).Run()

	s.Require().NoError(err)
	s.Require().NotNil(graph)

	s.Equal([]string{"Lake Erie"}, s.builtObjectsForPredicate("https://schema.org/name"))
	s.Equal([]string{"Lake Erie"}, s.builtObjectsForPredicate("http://www.w3.org/2000/01/rdf-schema#label"))
}

// TestBuildRejectsConfigurationTheModuleDoesNotDefine checks that a typo in a
// configuration property is caught by validation, since the module's vocabulary
// is what declares which properties configure its tasks.
func (s *SalModuleSuite) TestBuildRejectsConfigurationTheModuleDoesNotDefine() {
	s.newSalProject(projectReferencing(`fixture:regionn "west"`))

	_, err := (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg, Force: true}).Run()

	s.Require().Error(err)
	s.Contains(err.Error(), "regionn")
}

// TestRunReportsModuleErrors checks that a task which reports a
// salmodule:Error and exits non-zero fails the run with its own message
// rather than with the container's exit status.
func (s *SalModuleSuite) TestRunReportsModuleErrors() {
	project := s.newSalProject(projectReferencing(`fixture:fail "true"^^xsd:boolean`))
	s.buildForRun(project)

	_, err := (&build.RunCmd{Paths: []string{"module.ttl"}}).Run()

	s.Require().Error(err)
	s.Contains(err.Error(), "the task instance asked this module to fail")
}

// TestRunHasNothingToDoForClassesThatAreNotTasks checks that referencing a
// module term which is not a task builds cleanly without invoking the module's
// run command, and leaves sal run with nothing to run.
func (s *SalModuleSuite) TestRunHasNothingToDoForClassesThatAreNotTasks() {
	project := s.newSalProject(fmt.Sprintf(`
@prefix fixture: <%s> .

<Reference> a fixture:NotATask .
`, moduleNamespace))
	s.buildForRun(project)

	s.Empty(s.builtObjectsForPredicate("https://schema.org/name"))

	_, err := (&build.RunCmd{Paths: []string{"module.ttl"}}).Run()

	s.Require().ErrorIs(err, build.ErrNoModuleTasks)
}

// builtObjectsForPredicate reads the built Iceberg table and returns every
// object written for a predicate of a place the module produced. A build writes
// more than the module's output to the table: it also records DCAT metadata for
// the files in .sal/data, which includes the vocabulary documents the project
// pinned, and one of those triples is a schema:name of the document's digest.
// Restricting to the module's own subjects is what keeps these assertions about
// what the module produced.
func (s *SalModuleSuite) builtObjectsForPredicate(predicate string) []string {
	tbl, err := pkg.GetSalIcebergTable()
	s.Require().NoError(err)
	if tbl.CurrentSnapshot() == nil {
		return nil
	}

	// An object is stored in exactly one typed column. What a module produces
	// here is IRIs, strings, and numbers, so the geometry, byte, and time
	// columns are not read. Columns are resolved by name since a projection
	// comes back in schema order rather than selection order.
	_, records, err := tbl.Scan(
		table.WithSelectedFields("subject", "predicate", "object_iri", "object_float", "object_integer", "object_string"),
		table.WithCaseSensitive(true),
	).ToArrowRecords(context.Background())
	s.Require().NoError(err)

	var objects []string
	for record, err := range records {
		s.Require().NoError(err)
		if record == nil {
			continue
		}
		columns := map[string]int{}
		for i, field := range record.Schema().Fields() {
			columns[field.Name] = i
		}
		subjects := record.Column(columns["subject"]).(*array.String)
		predicates := record.Column(columns["predicate"]).(*array.String)
		iris := record.Column(columns["object_iri"]).(*array.String)
		floats := record.Column(columns["object_float"]).(*array.Float64)
		integers := record.Column(columns["object_integer"]).(*array.Int64)
		strs := record.Column(columns["object_string"]).(*array.String)
		for i := range int(record.NumRows()) {
			if !strings.HasPrefix(subjects.Value(i), placeNamespace) || predicates.Value(i) != predicate {
				continue
			}
			switch {
			case iris.IsValid(i):
				objects = append(objects, iris.Value(i))
			case floats.IsValid(i):
				objects = append(objects, strconv.FormatFloat(floats.Value(i), 'f', -1, 64))
			case integers.IsValid(i):
				objects = append(objects, strconv.FormatInt(integers.Value(i), 10))
			default:
				objects = append(objects, strs.Value(i))
			}
		}
		record.Release()
	}
	return objects
}
