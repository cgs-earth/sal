package pkg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
)

func SalIcebergCatalog() (catalog.Catalog, error) {
	dataDir, err := SalDataDir()
	if err != nil {
		return nil, err
	}
	cat, err := hadoop.NewCatalog("local-catalog", dataDir, nil)
	return cat, err
}

func GetSalIcebergTable() (*table.Table, error) {
	dataDir, err := SalDataDir()
	if err != nil {
		return nil, err
	}
	cat, err := SalIcebergCatalog()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	defaultNS := catalog.ToIdentifier(dataDir)
	if err := cat.CreateNamespace(ctx, defaultNS, nil); err != nil &&
		!errors.Is(err, catalog.ErrNamespaceAlreadyExists) {
		slog.Error("Failed to create default namespace:", "error", err)
		return nil, err
	}

	tableIdent := catalog.ToIdentifier(cfg.Namespace, "triples")
	if tbl, err := cat.LoadTable(ctx, tableIdent); err == nil {
		slog.Info("Loaded existing Iceberg table")
		return tbl, nil
	} else if !errors.Is(err, catalog.ErrNoSuchTable) {
		return nil, fmt.Errorf("load existing Iceberg table: %w", err)
	}
	tbl, err := cat.LoadTable(nil, table.Identifier{"default", "triples"})
	return tbl, err
}

func SalSnapshots() ([]string, error) {

	// tbl :=
	// for _, snap := range tbl.Metadata().Snapshots() {
	// 	fmt.Println(snap.SnapshotID)
	// }
}
