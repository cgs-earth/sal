//go:build integration

package salmoduleintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/build"
	"github.com/cgs-earth/sal/build/validate"
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

	// the fixture module's ontology changes with the fixture, so a vocabulary
	// cached by an earlier run would validate this run against the wrong terms
	s.Require().NoError(validate.ClearCache())

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
func (s *SalModuleSuite) cloneFixtureModule(ctx context.Context, dir string, name string, args ...string) error {
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
		return fmt.Errorf("%s %s: %s: %w", name, strings.Join(rewritten, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (s *SalModuleSuite) git(dir string, args ...string) {
	command := exec.Command("git", args...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	s.Require().NoErrorf(err, "git %s: %s", strings.Join(args, " "), string(out))
}

// newSalProject creates an initialized SAL project holding source, and makes it
// the working directory for the rest of the test.
func (s *SalModuleSuite) newSalProject(source string) string {
	project := s.T().TempDir()
	s.git(project, "init")
	s.git(project, "remote", "add", "origin", "https://github.com/cgs-earth/sal-integration-test-project.git")
	s.Require().NoError(os.WriteFile(filepath.Join(project, "module.ttl"), []byte(source), 0644))
	s.git(project, "add", ".")
	s.git(project, "-c", "user.email=sal@example.test", "-c", "user.name=SAL", "commit", "-m", "project source")

	s.Require().NoError(os.Chdir(project))
	s.Require().NoError((&initialization.InitCmd{}).Run())
	return project
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

// TestBuildMaterializesModuleTriples is the round trip: a project referencing
// the module is built, and the triples the module produced are read back out of
// the Iceberg table SAL wrote.
func (s *SalModuleSuite) TestBuildMaterializesModuleTriples() {
	s.newSalProject(projectReferencing())

	graph, err := (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg, Force: true}).Run()

	s.Require().NoError(err)
	s.Require().NotNil(graph)

	objects := s.builtObjectsForPredicate("https://schema.org/name")
	s.Contains(objects, "Lake Tahoe")
	s.Contains(objects, "Lake Erie")
}

// TestBuildConfiguresTheTaskWithItsRDFProperties is what makes the module's
// configuration RDF rather than an embedded JSON-LD literal: fixture:region is a
// property of the instance, and the places the build ends up with are only the
// ones the module emits for the region it was given.
func (s *SalModuleSuite) TestBuildConfiguresTheTaskWithItsRDFProperties() {
	s.newSalProject(projectReferencing(`fixture:region "west"`))

	graph, err := (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg, Force: true}).Run()

	s.Require().NoError(err)
	s.Require().NotNil(graph)

	objects := s.builtObjectsForPredicate("https://schema.org/name")
	s.Contains(objects, "Lake Tahoe")
	s.NotContains(objects, "Lake Erie")
}

// TestBuildConfiguresTheTaskWithTypedLiterals checks the same path for a
// property whose literal carries a datatype, which reaches the module as a
// JSON-LD value object rather than as a bare JSON value.
func (s *SalModuleSuite) TestBuildConfiguresTheTaskWithTypedLiterals() {
	s.newSalProject(projectReferencing(`fixture:region "east"`, `fixture:labelled "true"^^xsd:boolean`))

	graph, err := (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg, Force: true}).Run()

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

// TestBuildReportsModuleErrors checks that a task which reports a
// salmodule:Error and exits non-zero fails the build with its own message
// rather than with the container's exit status.
func (s *SalModuleSuite) TestBuildReportsModuleErrors() {
	s.newSalProject(projectReferencing(`fixture:fail "true"^^xsd:boolean`))

	_, err := (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg, Force: true}).Run()

	s.Require().Error(err)
	s.Contains(err.Error(), "the task instance asked this module to fail")
}

// TestBuildSkipsClassesThatAreNotTasks checks that referencing a module term
// which is not a task leaves the module's run command uninvoked.
func (s *SalModuleSuite) TestBuildSkipsClassesThatAreNotTasks() {
	s.newSalProject(fmt.Sprintf(`
@prefix fixture: <%s> .

<Reference> a fixture:NotATask .
`, moduleNamespace))

	graph, err := (&build.BuildCmd{Paths: []string{"module.ttl"}, Format: build.GraphExportFormatIceberg, Force: true}).Run()

	s.Require().NoError(err)
	s.Require().NotNil(graph)
	s.Empty(s.builtObjectsForPredicate("https://schema.org/name"))
}

// builtObjectsForPredicate reads the built Iceberg table and returns every
// object written for a predicate.
func (s *SalModuleSuite) builtObjectsForPredicate(predicate string) []string {
	tbl, err := pkg.GetSalIcebergTable()
	s.Require().NoError(err)
	if tbl.CurrentSnapshot() == nil {
		return nil
	}

	_, records, err := tbl.Scan(
		table.WithSelectedFields("predicate", "object"),
		table.WithCaseSensitive(true),
	).ToArrowRecords(context.Background())
	s.Require().NoError(err)

	var objects []string
	for record, err := range records {
		s.Require().NoError(err)
		if record == nil {
			continue
		}
		predicates := record.Column(0).(*array.String)
		values := record.Column(1).(*array.String)
		for i := range int(record.NumRows()) {
			if predicates.Value(i) == predicate {
				objects = append(objects, values.Value(i))
			}
		}
		record.Release()
	}
	return objects
}
