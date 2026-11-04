package main

import (
	"context"
	"log"

	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
)

func main() {
	ctx := context.Background()

	// Create / load Hadoop catalog (same warehouse as your loader)
	cat, err := hadoop.NewCatalog(ctx, "local-catalog", "/tmp/iceberg-warehouse", nil)
	if err != nil {
		log.Fatal("failed to create catalog:", err)
	}

	tableIdent := catalog.ToIdentifier("default", "triples")

	// Load existing table
	tbl, err := cat.LoadTable(ctx, tableIdent)
	if err != nil {
		log.Fatal("failed to load table:", err)
	}

	props := tbl.Properties()
	log.Println("Table properties:", props)

	predicate := iceberg.EqualTo(
		iceberg.Reference("subject"),
		"https://gleaner.io/id/org/https://geoconnex.us/sitemap/ca-gage-assessment/ca_gages_pids__0.xml",
	)

	// Delete matching rows
	newTable, err := tbl.Delete(ctx, predicate, nil)
	if err != nil {
		log.Fatal("delete failed:", err)
	}

	log.Println("Delete committed successfully")
	log.Println("New table snapshot:", newTable.CurrentSnapshot().SnapshotID)

	// -----------------------------
	// VERIFY: scan to ensure no rows remain
	// -----------------------------

	// Reload table to ensure latest snapshot
	tbl, err = cat.LoadTable(ctx, tableIdent)
	if err != nil {
		log.Fatal("failed to reload table:", err)
	}

	// Run scan using proper option-based API
	scan := tbl.Scan(
		table.WithRowFilter(
			iceberg.EqualTo(
				iceberg.Reference("subject"),
				"https://gleaner.io/id/org/https://geoconnex.us/sitemap/ca-gage-assessment/ca_gages_pids__0.xml",
			),
		),
		table.WithCaseSensitive(true),
	)

	/// Execute scan as Arrow records
	_, records, err := scan.ToArrowRecords(ctx)
	if err != nil {
		log.Fatal("scan failed:", err)
	}

	found := 0

	for batch, err := range records {
		if err != nil {
			log.Fatal("record iteration error:", err)
		}
		if batch != nil {
			found++
		}
	}

	// Verify deletion
	if found > 0 {
		log.Fatalf("DELETE VERIFICATION FAILED: still found %d record batch(es)", found)
	}

	log.Println("Verification passed: no matching rows found")
}
