package pkg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
)

const DefaultSalIcebergTable = "triples"

func SalIcebergCatalog() (catalog.Catalog, error) {
	dataDir, err := SalDataDir()
	if err != nil {
		return nil, err
	}
	cat, err := hadoop.NewCatalog("local-catalog", dataDir, nil)
	return cat, err
}

func GetSalIcebergTable() (*table.Table, error) {
	cat, err := SalIcebergCatalog()
	if err != nil {
		return nil, err
	}
	gitProjectName, err := GitProjectName()
	if err != nil {
		return nil, err
	}
	dataDir, err := SalDataDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir+"/"+gitProjectName, 0755); err != nil {
		slog.Error("Failed to create warehouse directory:", "error", err)
		return nil, err
	}

	ctx := context.Background()
	defaultNS := catalog.ToIdentifier(gitProjectName)
	if err := cat.CreateNamespace(ctx, defaultNS, nil); err != nil &&
		!errors.Is(err, catalog.ErrNamespaceAlreadyExists) {
		slog.Error("Failed to create default namespace:", "error", err)
		return nil, err
	}

	tableIdent := catalog.ToIdentifier(gitProjectName, DefaultSalIcebergTable)
	return cat.LoadTable(ctx, tableIdent)
}

func GetSalSnapshots() ([]string, error) {

	tbl, err := GetSalIcebergTable()
	if err != nil {
		return nil, err
	}
	var snapshots []string
	for _, s := range tbl.Metadata().Snapshots() {
		snapshots = append(snapshots, fmt.Sprintf("%d", s.SnapshotID))
	}
	return snapshots, nil
}
