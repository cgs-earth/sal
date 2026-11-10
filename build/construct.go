package build

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cgs-earth/sal/build/load"
	"github.com/cgs-earth/sal/construct"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	salsparql "github.com/cgs-earth/sal/query/sparql"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel/attribute"
)

// stagedNamespace is the Iceberg namespace of the temporary table a build
// that does not write the project's table stages its graph in.
const stagedNamespace = "staged"

// stageGraph writes a build where the project's CONSTRUCT queries can read it
// before it is committed, and returns a runner over it plus what removes
// anything that was written outside the project. An Iceberg build is staged
// from its spool on a branch of the project's own table, where only what the
// table does not hold yet is written, and the staged graph returned is what
// the build then commits and discards. Any other format has no table to stage
// in and no spool, so the whole graph goes to a temporary warehouse. Tests
// replace it.
var stageGraph = func(ctx context.Context, graph *rdflibgo.Graph, spool *load.Spool, format GraphExportFormat) (construct.Runner, *load.StagedGraph, func(), error) {
	if format == GraphExportFormatIceberg {
		cfg, err := projectLoadConfig()
		if err != nil {
			return nil, nil, nil, err
		}
		staged, err := load.StageSpool(ctx, spool, cfg)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("stage the build: %w", err)
		}
		return salsparql.DuckDBRunner{TablePath: staged.TablePath, SnapshotID: staged.SnapshotID}, staged, func() {}, nil
	}

	warehouse, err := os.MkdirTemp("", "sal-construct-*")
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() {
		if err := os.RemoveAll(warehouse); err != nil {
			slog.Warn("failed to remove the staged table", "path", warehouse, "error", err)
		}
	}
	err = load.WriteGraphToIceberg(ctx, graph, &load.LoadConfig{
		BatchSize:          131072,
		ParquetCompression: "snappy",
		Warehouse:          warehouse,
		Namespace:          stagedNamespace,
	}, nil, "")
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("stage the build: %w", err)
	}
	return salsparql.DuckDBRunner{TablePath: filepath.Join(warehouse, stagedNamespace, "triples")}, nil, cleanup, nil
}

// MaterializeConstructs runs the project's CONSTRUCT queries over the build
// and returns the triples they constructed, which the build commits in the
// same snapshot as everything else. The queries are answered by the same
// SPARQL to SQL translation `sal construct` and `sal serve` use, which reads
// a table, so the build is staged first: an Iceberg build from its spool, on
// a branch of the project's table, which leaves the table's main branch
// untouched until the build commits, and any other from graph. Every query
// reads the build as it stood before any of them ran, so the order they run
// in does not change what they construct. graph's blank nodes must be stable
// already, since a constructed triple refers to one by the label the staged
// table gives it, and nothing is renamed afterwards.
//
// The staged graph returned is nil when nothing was staged in the project's
// table; otherwise the caller commits the build through it, with the
// constructed triples added to the spool, and discards it.
func MaterializeConstructs(ctx context.Context, graph *rdflibgo.Graph, spool *load.Spool, projectDir string, queries []construct.Query, format GraphExportFormat) (_ *rdflibgo.Graph, _ *load.StagedGraph, err error) {
	if len(queries) == 0 {
		// nothing to run, but the directory is still wiped of what an earlier
		// build constructed
		_, err := construct.Run(ctx, nil, projectDir, nil)
		return nil, nil, err
	}
	ctx, span := telemetry.Start(ctx, "build.construct", attribute.Int("sal.construct.queries", len(queries)))
	defer func() { telemetry.End(span, err) }()

	slog.Info(fmt.Sprintf("Staging the build for %d SPARQL CONSTRUCT queries to read; the project's table is committed once they have run", len(queries)))
	runner, stagedGraph, cleanup, err := stageGraph(ctx, graph, spool, format)
	if err != nil {
		return nil, nil, fmt.Errorf("build: %w", err)
	}
	defer cleanup()

	constructed, err := construct.Run(ctx, runner, projectDir, queries)
	if err != nil {
		return nil, nil, errors.Join(err, stagedGraph.Discard(ctx))
	}
	span.SetAttributes(attribute.Int("sal.triples.constructed", pkg.GraphLen(constructed)))
	slog.Info(fmt.Sprintf("Constructed %d triples with %d SPARQL CONSTRUCT queries", pkg.GraphLen(constructed), len(queries)))
	return constructed, stagedGraph, nil
}
