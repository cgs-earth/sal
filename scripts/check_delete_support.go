package main

import (
	"context"
	"log"

	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
)

func main() {
	ctx := context.Background()

	// Create / load Hadoop catalog (same warehouse as your loader)
	cat, err := hadoop.NewCatalog("local-catalog", "/tmp/iceberg-warehouse", nil)
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

	newTable, err := tbl.Delete(
		ctx,
		iceberg.EqualTo(
			iceberg.Reference("subject"),
			"https://gleaner.io/id/org/https://geoconnex.us/sitemap/ca-gage-assessment/ca_gages_pids__0.xml",
		),
		nil, // snapshot properties (optional)
	)
	if err != nil {
		log.Fatal("delete failed:", err)
	}

	log.Println("Delete committed successfully")
	log.Println("New table snapshot:", newTable.CurrentSnapshot().SnapshotID)

}
