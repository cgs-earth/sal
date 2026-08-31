package load

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/table"
	"github.com/geoarrow/geoarrow-go"
)

// GetSchemas is the schema of a triples table, in both its Arrow and Iceberg
// forms. An object is split across one column per datatype -- IRI, string,
// geometry, byte, integer, float, timestamp -- so that a query compares and
// reads each kind natively rather than parsing it out of a single text column.
// object_type keeps the datatype IRI the literal was written with, so a typed
// literal round-trips back to RDF with its exact datatype even when its value
// is stored in a typed column (or when no typed column fits and it is stored
// as a string).
func GetSchemas() (*arrow.Schema, *iceberg.Schema, error) {
	geoCRS, err := json.Marshal("OGC:CRS84")
	if err != nil {
		return nil, nil, err
	}
	geoMetadata := geoarrow.Metadata{
		CRS:     geoCRS,
		CRSType: geoarrow.CRSTypeAuthorityCode,
	}
	var arrowSchema = arrow.NewSchema(
		[]arrow.Field{
			{Name: "subject", Type: arrow.BinaryTypes.String},
			{Name: "predicate", Type: arrow.BinaryTypes.String},
			{Name: "object_string", Type: arrow.BinaryTypes.String, Nullable: true},
			{Name: "object_iri", Type: arrow.BinaryTypes.String, Nullable: true},
			{Name: "object_geometry", Type: geoarrow.NewWKBType(geoarrow.WKBWithBinaryStorage(), geoarrow.WKBWithMetadata(geoMetadata)), Nullable: true},
			{Name: "object_byte", Type: arrow.PrimitiveTypes.Int32, Nullable: true},
			{Name: "object_integer", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
			{Name: "object_float", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
			// object_time carries no timezone: build normalizes every stored
			// xsd:dateTime to UTC, which keeps DuckDB's text rendering of the
			// column independent of the querying machine's timezone setting.
			{Name: "object_time", Type: &arrow.TimestampType{Unit: arrow.Microsecond}, Nullable: true},
			{Name: "object_type", Type: arrow.BinaryTypes.String, Nullable: true},
			{Name: "triple_hash", Type: arrow.BinaryTypes.String, Nullable: false},
		},
		nil,
	)
	geometry_type, err := iceberg.GeometryTypeOf("OGC:CRS84")
	if err != nil {
		return nil, nil, err
	}
	var icebergSchema = iceberg.NewSchemaWithIdentifiers(1, []int{11},
		iceberg.NestedField{ID: 1, Name: "subject", Type: iceberg.PrimitiveTypes.String, Required: true},
		iceberg.NestedField{ID: 2, Name: "predicate", Type: iceberg.PrimitiveTypes.String, Required: true},
		iceberg.NestedField{ID: 3, Name: "object_string", Type: iceberg.PrimitiveTypes.String, Required: false},
		iceberg.NestedField{ID: 4, Name: "object_iri", Type: iceberg.PrimitiveTypes.String, Required: false},
		iceberg.NestedField{ID: 5, Name: "object_geometry", Type: geometry_type, Required: false},
		// Iceberg has no 8-bit integer type, so object_byte is an int32 column
		// whose values build range-checks to xsd:byte before writing.
		iceberg.NestedField{ID: 6, Name: "object_byte", Type: iceberg.PrimitiveTypes.Int32, Required: false},
		iceberg.NestedField{ID: 7, Name: "object_integer", Type: iceberg.PrimitiveTypes.Int64, Required: false},
		iceberg.NestedField{ID: 8, Name: "object_float", Type: iceberg.PrimitiveTypes.Float64, Required: false},
		iceberg.NestedField{ID: 9, Name: "object_time", Type: iceberg.PrimitiveTypes.Timestamp, Required: false},
		iceberg.NestedField{ID: 10, Name: "object_type", Type: iceberg.PrimitiveTypes.String, Required: false},
		iceberg.NestedField{ID: 11, Name: "triple_hash", Type: iceberg.PrimitiveTypes.String, Required: true},
	)

	return arrowSchema, icebergSchema, nil

}

func NewIcebergTableFromCfg(ctx context.Context, tableSchema *iceberg.Schema, cat catalog.Catalog, cfg *LoadConfig) (*table.Table, error) {

	if err := os.MkdirAll(cfg.Warehouse+"/"+cfg.Namespace, 0755); err != nil {
		slog.Error("Failed to create warehouse directory:", "error", err)
		return nil, err
	}

	defaultNS := catalog.ToIdentifier(cfg.Namespace)
	if err := cat.CreateNamespace(ctx, defaultNS, nil); err != nil &&
		!errors.Is(err, catalog.ErrNamespaceAlreadyExists) {
		slog.Error("Failed to create default namespace:", "error", err)
		return nil, err
	}

	tableIdent := catalog.ToIdentifier(cfg.Namespace, "triples")
	if tbl, err := cat.LoadTable(ctx, tableIdent); err == nil {
		if _, ok := tbl.Schema().FindFieldByName("object_type"); !ok {
			return nil, fmt.Errorf("the existing triples table was built by an older sal without the object_type column; run `sal clean --wipe` and `sal build` to rebuild it")
		}
		slog.Info("Loaded existing Iceberg table")
		return tbl, nil
	} else if !errors.Is(err, catalog.ErrNoSuchTable) {
		return nil, fmt.Errorf("load existing Iceberg table: %w", err)
	}

	partitionSpec := iceberg.NewPartitionSpec(
		iceberg.PartitionField{
			SourceIDs: []int{2},
			Transform: iceberg.TruncateTransform{Width: 20},
			Name:      "predicate_partition",
		},
	)

	sortField := table.SortField{
		SourceIDs: []int{2},
		Transform: iceberg.IdentityTransform{},
		Direction: table.SortASC,
		NullOrder: table.NullsLast,
	}
	sortOrder, err := table.NewSortOrder(table.InitialSortOrderID, []table.SortField{sortField})
	if err != nil {
		return nil, err
	}

	properties := iceberg.Properties{
		table.MetadataDeleteAfterCommitEnabledKey: "true",
		table.MetadataPreviousVersionsMaxKey:      strconv.Itoa(1),
		table.ManifestMergeEnabledKey:             "true",
		table.ManifestMinMergeCountKey:            strconv.Itoa(1),
		"write.parquet.compression-codec":         cfg.ParquetCompression,
		"write.metadata.metrics.default":          "full",
		table.WriteDeleteModeKey:                  table.WriteModeMergeOnRead,
		// format version 3 is what the geometry type needs
		table.PropertyFormatVersion: "3",
		// (per-column toggle, prefix-matched; overrides the global switch)
		"write.parquet.dict-encoding-enabled.column.predicate": "true",
	}
	for k, v := range geometryMetricsProperty() {
		properties[k] = v
	}

	return cat.CreateTable(ctx, tableIdent, tableSchema,
		catalog.WithPartitionSpec(&partitionSpec),
		catalog.WithSortOrder(sortOrder),
		catalog.WithProperties(properties),
	)
}

func applyWriteProperties(ctx context.Context, tbl *table.Table, cfg *LoadConfig) error {
	writeProps := iceberg.Properties{
		"write.parquet.compression-codec": cfg.ParquetCompression,
	}
	for k, v := range geometryMetricsProperty() {
		writeProps[k] = v
	}

	txn := tbl.NewTransaction()
	if err := txn.SetProperties(writeProps); err != nil {
		return fmt.Errorf("set table properties: %w", err)
	}
	if _, err := txn.Commit(ctx); err != nil {
		return fmt.Errorf("commit table properties: %w", err)
	}
	return nil
}

func geometryMetricsProperty() iceberg.Properties {
	return iceberg.Properties{table.MetricsModeColumnConfPrefix + ".object_geometry": "full"}
}
