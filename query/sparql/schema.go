package sparql

import (
	"context"
	"fmt"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
)

// VerifyObjectColumns checks that the triples table has every column a query
// in this package reads: the typed object columns, object_language, and
// vocabulary, which the `triples` view filters on and is the newest of them,
// so it is the one checked. A table built by an older sal cannot be queried;
// it is reported as such rather than left to fail on a missing column inside
// DuckDB.
func VerifyObjectColumns(ctx context.Context, warehouse string, namespace string) error {
	cat, err := hadoop.NewCatalog("local-catalog", warehouse, nil)
	if err != nil {
		return fmt.Errorf("failed to create catalog: %w", err)
	}
	tbl, err := cat.LoadTable(ctx, table.Identifier{namespace, "triples"})
	if err != nil {
		return fmt.Errorf("load table: %w", err)
	}
	if _, ok := tbl.Schema().FindFieldByName("vocabulary"); !ok {
		return fmt.Errorf("the triples table was built by an older sal without the vocabulary column this version reads; run `sal clean --wipe` and `sal build` to rebuild it")
	}
	return nil
}
