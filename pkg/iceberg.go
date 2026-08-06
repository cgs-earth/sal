package pkg

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
)

const DefaultSalIcebergTable = "triples"

// IcebergTablePaths returns the root directory of every Iceberg table under
// root. A table root is the parent of a `metadata` directory holding at least
// one metadata JSON file. A root that does not exist holds no tables rather
// than being an error, since a project may never have written the directory
// being asked about.
func IcebergTablePaths(root string) ([]string, error) {
	var tables []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() || path == root || entry.Name() != "metadata" {
			return nil
		}
		matches, err := filepath.Glob(filepath.Join(path, "*.metadata.json"))
		if err != nil {
			return err
		}
		if len(matches) > 0 {
			tables = append(tables, filepath.Dir(path))
			return filepath.SkipDir
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find Iceberg tables in %s: %w", root, err)
	}
	return tables, nil
}

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

func SetTagOfLatestSnapshot(tbl *table.Table, cat catalog.Catalog) error {
	newSnapshot := tbl.CurrentSnapshot()
	if newSnapshot == nil {
		return fmt.Errorf("failed to get latest snapshot")
	}

	latestGitHash, err := GitCommitHash()
	if err != nil {
		return err
	}

	update := table.NewSetSnapshotRefUpdate(
		latestGitHash,
		newSnapshot.SnapshotID,
		table.TagRef,
		0,
		0,
		0,
	)

	_, _, err = cat.CommitTable(context.Background(), tbl.Identifier(), nil, []table.Update{update})
	return err
}
