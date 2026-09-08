package build

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
)

type ValidateCmd struct {
	Paths                           []string `arg:"positional" help:"RDF files to validate"`
	PrefixMaps                      []string `arg:"--prefix-maps" help:"prefix mappings to apply as source target pairs or source=target entries"`
	NoCache                         bool     `arg:"--no-cache" help:"resolve vocab prefixes from their remote sources instead of the versions pinned in .sal/config.jsonld"`
	AllowPrefixesWithoutSlashOrHash bool     `arg:"--allow-prefixes-without-slash-or-hash" help:"include a prefix whose namespace does not end in / or # without asking"`
}

func (cfg *ValidateCmd) Run() (*rdflibgo.Graph, error) {
	buildCfg := &BuildCmd{
		Paths:                           cfg.Paths,
		PrefixMaps:                      cfg.PrefixMaps,
		NoCache:                         cfg.NoCache,
		AllowPrefixesWithoutSlashOrHash: cfg.AllowPrefixesWithoutSlashOrHash,
		skipCommit:                      true,
		skipProjectChecks:               true,
	}
	return buildCfg.Run()
}

type BuildCmd struct {
	Paths                           []string          `arg:"positional" help:"RDF files to validate"`
	PrefixMaps                      []string          `arg:"--prefix-maps" help:"prefix mappings to apply as source target pairs or source=target entries"`
	Format                          GraphExportFormat `arg:"--format" help:"output format: nq or iceberg" default:"iceberg"`
	Force                           bool              `arg:"--force" help:"force build even if there are uncommitted changes in the git repository"`
	NoCache                         bool              `arg:"--no-cache" help:"resolve vocab prefixes from their remote sources and re-pin them in .sal/config.jsonld"`
	AllowPrefixesWithoutSlashOrHash bool              `arg:"--allow-prefixes-without-slash-or-hash" help:"include a prefix whose namespace does not end in / or # without asking"`

	// skip committing the built data to iceberg
	skipCommit bool

	// skip validating that the command is called from within a valid sal project / git repo
	skipProjectChecks bool

	// runModules materializes the SAL module tasks the project declares before
	// committing. Only `sal run` sets this; `sal build` commits the task
	// configuration without running anything.
	runModules bool
}

var findSALProjectDir = pkg.SALProjectDir

// confirm asks the user a yes/no question on the terminal; tests replace it
var confirm = pkg.Confirm

// ErrPrefixRejected is returned when the user declines to include a prefix
// whose namespace does not end in / or #. The prefix and the files declaring
// it are logged before it is returned.
var ErrPrefixRejected = errors.New("build: refused a prefix whose namespace does not end in / or #; fix the namespace, or pass --allow-prefixes-without-slash-or-hash to include it as written")

// ErrConflictingPrefixes is returned when the source files declare one
// vocabulary under namespaces mixing http and https, or with and without a
// trailing / or #. The conflict itself is logged with every file involved
// before it is returned.
var ErrConflictingPrefixes = errors.New("build: a vocabulary is declared under namespaces that mix http and https or a trailing / or #; declare one namespace everywhere or fold them together with --prefix-maps")

var ErrUncommittedChanges = fmt.Errorf("git repository has uncommitted changes; please commit and finalize changes before creating a new build snapshot")

// confirmPrefixesWithoutTerminator warns about every declared prefix whose
// namespace ends in neither / nor #, and asks before including each one unless
// the user passed --allow-prefixes-without-slash-or-hash. It runs once every file is parsed and before any
// term is checked, so nothing is fetched for a prefix the user then refuses.
func confirmPrefixesWithoutTerminator(validator *validate.Validator, allow bool) error {
	for _, prefix := range validator.PrefixesWithoutTerminator() {
		question := fmt.Sprintf("Detected prefix <%s> in %s which does not end in a / or #, so its terms will be joined straight onto it. Are you sure you want to include this?", prefix.Namespace, strings.Join(prefix.Paths, ", "))
		if allow {
			slog.Warn(question + " Including it because --allow-prefixes-without-slash-or-hash was passed.")
			continue
		}
		if !confirm(question) {
			slog.Error("Refused prefix whose namespace does not end in / or #", "namespace", prefix.Namespace, "declared_in", prefix.Paths)
			return ErrPrefixRejected
		}
	}
	return nil
}

// Run validates RDF files for terms that are not defined by their vocabularies and returns their merged RDF graph.
func (cfg *BuildCmd) Run() (*rdflibgo.Graph, error) {
	if cfg == nil {
		return nil, fmt.Errorf("build: missing arguments")
	}

	hasChanges, err := pkg.UncommittedChangesInGit()
	if err != nil {
		return nil, err
	}
	if hasChanges && !cfg.Force {
		return nil, ErrUncommittedChanges
	}
	if cfg.Force {
		slog.Warn("Creating build with modified source tree. This should only be done for testing purposes.")
	}

	pins, err := projectVocabularies(cfg.NoCache)
	if err != nil {
		return nil, err
	}

	// a module pinned in .sal/config.jsonld whose image is still on the docker
	// daemon is reused rather than cloned and built again; --no-cache resolves
	// every module from source, the same way it refetches every vocabulary
	resolver := salmodule.Default()
	if !cfg.NoCache {
		for namespace, commit := range pins.PinnedModuleCommits() {
			resolver.UsePinnedCommit(namespace, commit)
		}
	}

	var paths []string
	if len(cfg.Paths) > 0 {
		paths = cfg.Paths
	} else if !cfg.skipProjectChecks {
		projectDir, err := findSALProjectDir(os.UserHomeDir)
		if err != nil {
			return nil, fmt.Errorf("build: find SAL project directory: %w", err)
		}
		paths = []string{projectDir}
	} else {
		// if we are validating rdf data not in a valid sal project, use the current directory
		// as the default
		paths = []string{"."}
	}

	files, err := pkg.FindRdfDataInPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no JSON-LD or TTL files found in %s", strings.Join(paths, ", "))
	}

	vocabsToReplace, err := parsePrefixMaps(cfg.PrefixMaps)
	if err != nil {
		return nil, err
	}

	var base string
	if !cfg.skipProjectChecks {
		base, err = pkg.DefaultSalBase()
		if err != nil {
			return nil, err
		}
	} else {
		// if we are validating rdf data not in a valid sal project, use an empty base
		// since there is no git repo to check against
		base = ""
	}

	// the project ontology node in .sal/config.jsonld is validated and hashed
	// like any other source file, even though it has no file of its own to
	// pass to FindRdfDataInPaths, which skips .sal entirely
	var ontologyContent []byte
	if !cfg.skipProjectChecks {
		if ontologyContent, err = projectOntologyContent(base); err != nil {
			return nil, err
		}
	}

	hash, err := pkg.HashFilesAndContent(files, ontologyContent)
	if err != nil {
		return nil, err
	}

	// every file is parsed before any term is checked, so that the prefixes
	// they declare can be inspected, and the user asked about the doubtful
	// ones, before a single vocabulary is fetched to resolve a term against
	validator := validate.NewValidator(pins, base, vocabsToReplace)
	var docs []*validate.Document
	var errs validate.MultiError
	for _, file := range files {
		doc, err := validator.ParseFile(file)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		docs = append(docs, doc)
	}
	if ontologyContent != nil {
		configPath, err := pkg.SalConfigPath()
		if err != nil {
			return nil, err
		}
		doc, err := validator.ParseContent(ontologyContent, configPath)
		if err != nil {
			errs = append(errs, err)
		} else {
			docs = append(docs, doc)
		}
	}
	// a vocabulary declared under mixed spellings is one error, logged with
	// every file involved, so the summary error can stay short
	for _, conflict := range validator.ConflictingPrefixes() {
		var mixes []string
		if conflict.MixesSchemes {
			mixes = append(mixes, "http and https")
		}
		if conflict.MixesTerminators {
			mixes = append(mixes, "with and without a trailing / or #")
		}
		attrs := []any{"vocabulary", conflict.Vocabulary, "mixes", strings.Join(mixes, ", ")}
		for _, spelling := range conflict.Spellings {
			attrs = append(attrs, spelling.Namespace, spelling.Paths)
		}
		slog.Error("Vocabulary is declared under mixed namespaces, so its terms would not match across files", attrs...)
		errs = append(errs, ErrConflictingPrefixes)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if err := confirmPrefixesWithoutTerminator(validator, cfg.AllowPrefixesWithoutSlashOrHash); err != nil {
		return nil, err
	}

	finalGraph := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	for _, doc := range docs {
		// TODO do this in parallel.
		graph, err := validator.Validate(doc)
		if err != nil {
			if nested, ok := err.(validate.MultiError); ok {
				errs = append(errs, nested...)
			} else {
				errs = append(errs, err)
			}
			continue
		}
		mergeGraph(finalGraph, graph)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	validatedCount := len(docs)
	// a prefix a file declares is pinned even when no term from it was used, so
	// that what a project resolves against is what it declares
	if err := validator.PinDeclaredPrefixes(); err != nil {
		return nil, err
	}
	if validatedCount == 1 {
		slog.Info("Validated 1 file")
	} else {
		slog.Info("Validated " + fmt.Sprint(validatedCount) + " files")
	}

	if !cfg.skipProjectChecks {
		if err := NewTermsHaveClassDefinitions(finalGraph, base); err != nil {
			return nil, err
		}
	}

	if cfg.skipCommit {
		return finalGraph, nil
	}
	if cfg.Format == GraphExportFormatNQuads {
		slog.Warn("Exporting as NQuads. Note this will create a larger and less efficient file than iceberg")
	}

	if err := ImportOntologies(finalGraph, pins); err != nil {
		return nil, err
	}

	// the pins are written only once the build is known to be committable, so
	// that a rejected build does not leave the worktree dirty with a lockfile
	// change the next build would then refuse to run against
	if err := pins.Save(); err != nil {
		return nil, fmt.Errorf("build: record pinned vocabularies: %w", err)
	}

	// every vocabulary the project pins becomes provenance in the graph itself,
	// so which exact version a build validated against is queryable alongside
	// the data rather than only recorded in .sal/config.jsonld
	pins.AppendProvenance(finalGraph)

	// a build validates the task configuration of every referenced module
	// against the module's ontology, but only `sal run` invokes their run
	// commands; the configuration itself is committed like any other RDF
	if cfg.runModules {
		tasksRun, err := MaterializeSalModules(context.Background(), finalGraph, resolver)
		if err != nil {
			return nil, err
		}
		if tasksRun == 0 {
			return nil, ErrNoModuleTasks
		}
	}

	// every module downloaded so far, both the ones validation dereferenced for
	// their vocabulary and the ones materialization ran, is recorded in the table
	if err := ExportGraph(finalGraph, cfg.Format, hash, resolver.Downloaded()); err != nil {
		return nil, err
	}

	return finalGraph, err
}

// projectVocabularies opens the vocabulary versions the SAL project being built
// has pinned. RDF validated outside a SAL project has no project to pin
// anything for, so it resolves its vocabularies without recording them.
var projectVocabularies = func(refresh bool) (*validate.PinnedVocabularies, error) {
	path, err := pkg.SalConfigPath()
	if errors.Is(err, pkg.ErrSalDirNotFound) || errors.Is(err, pkg.ErrCantMakeSalDirInHome) {
		return validate.EphemeralVocabularies(), nil
	}
	if err != nil {
		return nil, err
	}
	blobsDir, err := pkg.SalBlobsDir()
	if err != nil {
		return nil, err
	}

	pins, err := validate.LoadPinnedVocabularies(path, blobsDir)
	if err != nil {
		return nil, fmt.Errorf("build: read pinned vocabularies: %w", err)
	}
	pins.Refresh = refresh
	return pins, nil
}

func parsePrefixMaps(values []string) (map[string]string, error) {
	mappings := map[string]string{}
	for i := 0; i < len(values); i++ {
		value := strings.TrimSpace(values[i])
		if value == "" {
			continue
		}
		if source, target, ok := strings.Cut(value, "="); ok {
			source = strings.TrimSpace(source)
			target = strings.TrimSpace(target)
			if source == "" || target == "" {
				return nil, fmt.Errorf("build: invalid prefix mapping %q", value)
			}
			mappings[source] = target
			continue
		}
		if i+1 >= len(values) {
			return nil, fmt.Errorf("build: prefix mapping %q missing target", value)
		}
		target := strings.TrimSpace(values[i+1])
		if target == "" {
			return nil, fmt.Errorf("build: prefix mapping %q missing target", value)
		}
		mappings[value] = target
		i++
	}
	return mappings, nil
}
