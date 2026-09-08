package sparql

import (
	"context"
	"fmt"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
)

// VerifyObjectColumns checks that the triples table splits its objects across
// the typed columns every query in this package reads, object_language
// included since it is the newest of them. A table built by a version of sal
// that still had the single `object` column, or that predates the language
// column, cannot be queried; it is reported as such rather than left to fail
// on a missing column inside DuckDB.
func VerifyObjectColumns(ctx context.Context, warehouse string, namespace string) error {
	cat, err := hadoop.NewCatalog("local-catalog", warehouse, nil)
	if err != nil {
		return fmt.Errorf("failed to create catalog: %w", err)
	}
	tbl, err := cat.LoadTable(ctx, table.Identifier{namespace, "triples"})
	if err != nil {
		return fmt.Errorf("load table: %w", err)
	}
	if _, ok := tbl.Schema().FindFieldByName("object_language"); !ok {
		return fmt.Errorf("the triples table was built by an older sal without the typed object columns and the object_language column this version reads; run `sal clean --wipe` and `sal build` to rebuild it")
	}
	return nil
}
