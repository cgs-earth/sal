package build

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cgs-earth/sal/build/load"
	"github.com/cgs-earth/sal/construct"
	"github.com/cgs-earth/sal/pkg/telemetry"
	salsparql "github.com/cgs-earth/sal/query/sparql"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel/attribute"
)

// stagedNamespace is the Iceberg namespace of the table a build stages its
// graph in for the project's CONSTRUCT queries to read.
const stagedNamespace = "staged"

// stageGraph writes a graph to a triples table in a temporary warehouse and
// returns a runner over it, plus what removes the warehouse again. It is what
// lets a CONSTRUCT query read a build that has not been committed yet; tests
// replace it.
var stageGraph = func(ctx context.Context, graph *rdflibgo.Graph) (construct.Runner, func(), error) {
	warehouse, err := os.MkdirTemp("", "sal-construct-*")
	if err != nil {
		return nil, nil, err
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
	}, nil)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("stage the build: %w", err)
	}
	return salsparql.DuckDBRunner{TablePath: filepath.Join(warehouse, stagedNamespace, "triples")}, cleanup, nil
}

// MaterializeConstructs runs the project's CONSTRUCT queries over the graph
// being built and returns the graph with the triples they constructed added,
// so that a build commits them in the same snapshot as everything else. The
// queries are answered by the same SPARQL to SQL translation `sal construct`
// and `sal serve` use, which reads a table, so the graph is staged in a
// temporary one; the project's own table is not touched until the build
// commits. Every query reads the graph as it stood before any of them ran, so
// the order they run in does not change what they construct.
//
// The graph returned has its blank nodes renamed the way the table names
// them, since that is how a constructed triple refers to one.
func MaterializeConstructs(ctx context.Context, graph *rdflibgo.Graph, projectDir string, queries []construct.Query) (_ *rdflibgo.Graph, err error) {
	if len(queries) == 0 {
		// nothing to run, but the directory is still wiped of what an earlier
		// build constructed
		_, err := construct.Run(ctx, nil, projectDir, nil)
		return graph, err
	}
	ctx, span := telemetry.Start(ctx, "build.construct", attribute.Int("sal.construct.queries", len(queries)))
	defer func() { telemetry.End(span, err) }()

	slog.Info(fmt.Sprintf("Staging the build in a temporary table for %d SPARQL CONSTRUCT queries to read; the project's table is written once they have run", len(queries)))
	staged := load.StabilizeBlankNodes(graph)
	runner, cleanup, err := stageGraph(ctx, staged)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	defer cleanup()

	constructed, err := construct.Run(ctx, runner, projectDir, queries)
	if err != nil {
		return nil, err
	}
	before := staged.Len()
	mergeGraph(staged, constructed)
	span.SetAttributes(attribute.Int("sal.triples.constructed", staged.Len()-before))
	slog.Info(fmt.Sprintf("Added %d triples constructed by %d SPARQL CONSTRUCT queries", staged.Len()-before, len(queries)))
	return staged, nil
}
