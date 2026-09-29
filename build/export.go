package build

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cgs-earth/sal/build/load"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/nq"
)

type GraphExportFormat string

const (
	GraphExportFormatNQuads  GraphExportFormat = "nq"
	GraphExportFormatIceberg GraphExportFormat = "iceberg"
)

// Export graph takes in a rdflib format graph struct and
// serializes it to disk in the specified format. modules are the SAL modules
// the build downloaded, which are recorded in the Iceberg table metadata.
// staged is the build as MaterializeConstructs staged it in the project's
// table, whose data files an Iceberg export commits rather than writing them
// again; it is nil for a build that staged nothing there.
func ExportGraph(ctx context.Context, graph *rdflibgo.Graph, format GraphExportFormat, hash string, modules []string, staged *load.StagedGraph) error {

	switch format {
	case "nq":
		dataDir, err := pkg.SalDataDir()
		if err != nil {
			return err
		}
		gitProject, err := pkg.GitProjectName()
		if err != nil {
			return err
		}
		fullOutPath := filepath.Join(dataDir, fmt.Sprintf("%s.nq", gitProject))
		file, err := os.Create(fullOutPath)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()

		err = nq.Serialize(graph, file)
		if err == nil {
			slog.Info("Saved built RDF data to " + fullOutPath)
		}
	case "iceberg":
		cfg, err := projectLoadConfig()
		if err != nil {
			return err
		}
		properties, err := icebergTableProperties(hash, modules)
		if err != nil {
			return err
		}
		if staged != nil {
			err = staged.Commit(ctx, graph, properties)
		} else {
			err = load.WriteGraphToIceberg(ctx, graph, cfg, properties)
		}
		if err != nil {
			return err
		}
		slog.Info("Saved built RDF data to Iceberg", "warehouse", cfg.Warehouse, "namespace", cfg.Namespace)
	default:
		return fmt.Errorf("unknown output format: '%s'. Must be iceberg or nq", format)
	}

	return nil
}

// projectLoadConfig is how a build writes the project's triples table, both
// when it stages the build there and when it commits it.
func projectLoadConfig() (*load.LoadConfig, error) {
	dataDir, err := pkg.SalDataDir()
	if err != nil {
		return nil, err
	}
	gitProject, err := pkg.GitProjectName()
	if err != nil {
		return nil, err
	}
	return &load.LoadConfig{
		BatchSize:          131072,
		ParquetCompression: "snappy",
		Warehouse:          dataDir,
		Namespace:          gitProject,
	}, nil
}

// icebergTableProperties describes a build in the Iceberg table metadata. The
// module list is written even when it is empty, so that a build which no longer
// downloads a module clears what an earlier build recorded.
func icebergTableProperties(hash string, modules []string) (map[string]string, error) {
	if modules == nil {
		modules = []string{}
	}
	encodedModules, err := json.Marshal(modules)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"sal.hash":                     hash,
		salmodule.IcebergTableProperty: string(encodedModules),
	}, nil
}
