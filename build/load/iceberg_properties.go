package load

import (
	"context"
	"crypto/sha256"
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
// as a string). object_language keeps the language tag of an rdf:langString
// literal, and is NULL for every other object, so a language-tagged literal
// round-trips too. triple_hash is the row's 32-byte SHA-256 itself, not its hex,
// since it is the largest column of the table and unique per row.
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
			{Name: "object_language", Type: arrow.BinaryTypes.String, Nullable: true},
			{Name: "triple_hash", Type: tripleHashArrowType, Nullable: false},
		},
		nil,
	)
	geometry_type, err := iceberg.GeometryTypeOf("OGC:CRS84")
	if err != nil {
		return nil, nil, err
	}
	var icebergSchema = iceberg.NewSchemaWithIdentifiers(1, []int{12},
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
		iceberg.NestedField{ID: 11, Name: "object_language", Type: iceberg.PrimitiveTypes.String, Required: false},
		iceberg.NestedField{ID: 12, Name: "triple_hash", Type: iceberg.MustFixedTypeOf(sha256.Size), Required: true},
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
		if _, ok := tbl.Schema().FindFieldByName("object_language"); !ok {
			return nil, fmt.Errorf("the existing triples table was built by an older sal without the object_language column; run `sal clean --wipe` and `sal build` to rebuild it")
		}
		// one release carried the statements of every pinned vocabulary in a
		// column of their own; a table with that column holds rows this sal
		// never writes and cannot be diffed against the graph it builds
		if _, ok := tbl.Schema().FindFieldByName("vocabulary"); ok {
			return nil, fmt.Errorf("the existing triples table was built by a sal that wrote a vocabulary column, which this version does not; run `sal clean --wipe` and `sal build` to rebuild it")
		}
		// a hash is a fixed[32] digest since the hex string it was cost the
		// table more bytes than any other column; Iceberg cannot evolve a
		// string column to fixed, and an equality delete must match the type
		// of the rows it deletes
		if field, ok := tbl.Schema().FindFieldByName("triple_hash"); ok && !field.Type.Equals(iceberg.MustFixedTypeOf(sha256.Size)) {
			return nil, fmt.Errorf("the existing triples table was built by an older sal that stored triple_hash as a hex string; run `sal clean --wipe` and `sal build` to rebuild it")
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

	// the order spoolDiff.added writes rows in; iceberg-go also sorts each
	// batch it writes by it
	sortOrder, err := table.NewSortOrder(table.InitialSortOrderID, []table.SortField{
		{SourceIDs: []int{2}, Transform: iceberg.IdentityTransform{}, Direction: table.SortASC, NullOrder: table.NullsLast},
		{SourceIDs: []int{1}, Transform: iceberg.IdentityTransform{}, Direction: table.SortASC, NullOrder: table.NullsLast},
	})
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
	}
	for k, v := range columnWriteProperties() {
		properties[k] = v
	}

	return cat.CreateTable(ctx, tableIdent, tableSchema,
		catalog.WithPartitionSpec(&partitionSpec),
		catalog.WithSortOrder(sortOrder),
		catalog.WithProperties(properties),
	)
}

// rowGroupRows is how many rows a row group of a data or delete file holds at
// most. A row group is buffered whole before it is written, so it is kept to
// the size DuckDB itself writes rather than iceberg-go's default of a million
// rows, which for rows carrying geometries is gigabytes.
const rowGroupRows = 122880

func applyWriteProperties(ctx context.Context, tbl *table.Table, cfg *LoadConfig) error {
	writeProps := iceberg.Properties{
		"write.parquet.compression-codec": cfg.ParquetCompression,
		table.ParquetRowGroupLimitKey:     strconv.Itoa(rowGroupRows),
	}
	for k, v := range columnWriteProperties() {
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

// columnWriteProperties are the per-column write settings:
//   - full metrics for the geometry, so its bounding box is never truncated;
//   - dictionary encoding for the predicate, which repeats on every row of a
//     partition, and none for triple_hash and object_geometry, whose values
//     are unique or nearly so, so a dictionary of them is only ever started
//     and abandoned, nor for the numeric and time columns (object_byte,
//     object_integer, object_float, object_time), which are almost always
//     null and, where set, mostly measured values that rarely repeat; their
//     filters are pruned by min/max statistics, which a dictionary does not
//     change;
//   - a bloom filter on subject and object_iri, the columns a SPARQL pattern
//     binds to a constant IRI (`subject = '...'`, `object_iri = '...'`) or
//     joins on. DuckDB probes the filter of each row group its bounds could
//     not exclude, for a constant or for the few keys of a join it pushes into
//     the scan, and skips the row group when the IRI is not in it. Rows are
//     sorted by predicate then subject, so a row group's object_iri bounds
//     still span nearly every IRI and only its filter rules one out; its
//     subject bounds are already narrow, and the subject filter only rules out
//     a row group whose range holds an IRI that has none of its triples.
//
// iceberg-go gives arrow-go no estimate of a column's distinct values, so
// every bloom filter is written at bloomFilterMaxBytes whatever the row
// group holds; see bloomFilterMaxBytes.
func columnWriteProperties() iceberg.Properties {
	return iceberg.Properties{
		table.MetricsModeColumnConfPrefix + ".object_geometry":         "full",
		dictEncodingColumnPrefix + ".predicate":                        "true",
		dictEncodingColumnPrefix + ".triple_hash":                      "false",
		dictEncodingColumnPrefix + ".object_geometry":                  "false",
		dictEncodingColumnPrefix + ".object_byte":                      "false",
		dictEncodingColumnPrefix + ".object_integer":                   "false",
		dictEncodingColumnPrefix + ".object_float":                     "false",
		dictEncodingColumnPrefix + ".object_time":                      "false",
		table.ParquetBloomFilterColumnEnabledKeyPrefix + ".subject":    "true",
		table.ParquetBloomFilterColumnEnabledKeyPrefix + ".object_iri": "true",
		table.ParquetBloomFilterMaxBytesKey:                            strconv.Itoa(bloomFilterMaxBytes),
	}
}

// bloomFilterMaxBytes is the size of every bloom filter, since iceberg-go
// writes each one at its maximum. 128 KiB is about 8.5 bits for each row of a
// full row group (rowGroupRows), a false positive rate near 2% for a row group
// of distinct IRIs; iceberg-go's default of 1 MiB per column per row group
// would be more than the rest of a file of a few thousand rows.
const bloomFilterMaxBytes = 128 * 1024

// dictEncodingColumnPrefix is the iceberg-go property prefix that turns
// dictionary encoding on or off for one column.
const dictEncodingColumnPrefix = "write.parquet.dict-encoding-enabled.column"

// tripleHashArrowType is the Arrow type of the triple_hash column.
var tripleHashArrowType = &arrow.FixedSizeBinaryType{ByteWidth: sha256.Size}
