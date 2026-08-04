package serve

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	salsparql "github.com/cgs-earth/sal/query/sparql"
)

type ServeCmd struct {
	WithUI bool `arg:"--with-ui" help:"Serve the SAL web UI at / with stats, SQL, SPARQL, module, and map tabs"`
}

func (cmd *ServeCmd) Run() error {
	if cmd == nil {
		return fmt.Errorf("serve: missing arguments")
	}
	warehouse, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(warehouse)
	if err != nil {
		return fmt.Errorf("failed to read SAL data directory: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no data has been built yet; run `sal build` to build a data product first")
	}

	namespace, err := pkg.GitProjectName()
	if err != nil {
		return err
	}

	layout, err := salsparql.ObjectLayoutForTable(context.Background(), warehouse, namespace)
	if err != nil {
		return err
	}
	tablePath := joinRemote(warehouse, namespace, "triples")
	return Serve(context.Background(), ":8080", tablePath, layout, cmd.WithUI)
}

func joinRemote(base string, parts ...string) string {
	joined := path.Join(parts...)
	if joined == "." {
		return strings.TrimSuffix(base, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + joined
}
