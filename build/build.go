package build

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

type ValidateCmd struct {
	Paths      []string          `arg:"positional" help:"RDF files to validate"`
	PrefixMaps []string          `arg:"--prefix-maps" help:"prefix mappings to apply as source target pairs or source=target entries"`
	Format     GraphExportFormat `arg:"--format" help:"output format: nq or iceberg" default:"iceberg"`
}

func (cfg *ValidateCmd) Run() (*rdflibgo.Graph, error) {
	buildCfg := &BuildCmd{
		Paths:             cfg.Paths,
		PrefixMaps:        cfg.PrefixMaps,
		Format:            cfg.Format,
		skipCommit:        true,
		skipProjectChecks: true,
	}
	return Run(buildCfg)
}

type BuildCmd struct {
	Paths        []string          `arg:"positional" help:"RDF files to validate"`
	PrefixMaps   []string          `arg:"--prefix-maps" help:"prefix mappings to apply as source target pairs or source=target entries"`
	Format       GraphExportFormat `arg:"--format" help:"output format: nq or iceberg" default:"iceberg"`
	Force        bool              `arg:"--force" help:"force build even if there are uncommitted changes in the git repository"`
	DataTypeCols bool              `arg:"--typed" help:"Split distinct data types into separate columns" default:"false"`

	// skip committing the built data to iceberg
	skipCommit bool

	// skip validating that the command is called from within a valid sal project / git repo
	skipProjectChecks bool
}

var findSALProjectDir = pkg.SALProjectDir

var ErrUncommittedChanges = fmt.Errorf("git repository has uncommitted changes; please commit and finalize changes before creating a new build snapshot")

// Run validates RDF files for terms that are not defined by their vocabularies and returns their merged RDF graph.
func Run(cfg *BuildCmd) (*rdflibgo.Graph, error) {
	if cfg == nil {
		return nil, fmt.Errorf("build: missing arguments")
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

	hash, err := pkg.HashAllFiles(files)
	if err != nil {
		return nil, err
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

	finalGraph := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	var errs validate.MultiError
	for _, file := range files {
		// TODO do this in parallel.
		graph, err := validate.ValidateRDFFile(file, vocabsToReplace, base)
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
	if len(files) == 1 {
		slog.Info("Validated 1 file")
	} else {
		slog.Info("Validated " + fmt.Sprint(len(files)) + " files")
	}

	if !cfg.skipProjectChecks {
		if err := NewTermsHaveClassDefinitions(finalGraph, base); err != nil {
			return nil, err
		}
	}
	if cfg.Format == GraphExportFormatNQuads {
		slog.Warn("Exporting as NQuads. Note this will create a larger and less efficient file than iceberg")
	}

	if cfg.skipCommit {
		return finalGraph, nil
	}
	hasChanges, err := pkg.UncommittedChangesInGit()
	if err != nil {
		return nil, err
	}
	if hasChanges && !cfg.Force {
		return finalGraph, ErrUncommittedChanges
	}
	if cfg.Force {
		slog.Warn("Creating build with modified source tree. This should only be done for testing purposes.")
	}

	if err := AddAdditionalDataFileMetadata(finalGraph); err != nil {
		return nil, err
	}

	if err := ExportGraph(finalGraph, cfg.Format, hash, cfg.DataTypeCols); err != nil {
		return nil, err
	}

	return finalGraph, err
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
