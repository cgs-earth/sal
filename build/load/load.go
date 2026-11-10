package load

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/telemetry"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel/attribute"
)

type LoadConfig struct {
	// BatchSize is the number of Arrow records to write per batch.
	BatchSize int

	// Workers is the number of input files to convert to Parquet in parallel.
	Workers int

	// ParquetCompression is the Parquet compression codec
	// (snappy, zstd, gzip, brotli, lz4, or uncompressed).
	ParquetCompression string

	// InputDir is the path to a directory containing .nq.gz files.
	InputDir string

	// MaxFiles is the maximum number of input files to process.
	// A value of 0 processes all files.
	MaxFiles int

	// Warehouse is the Iceberg warehouse directory.
	Warehouse string

	// Namespace is the Iceberg namespace.
	Namespace string
}

// ScratchDir is the directory, under a warehouse, that a build's spool and
// the DuckDB diff over it write their scratch files to. It is on the same
// disk as the table the rows end up in, and removed once the build is done.
const ScratchDir = pkg.ScratchDirName

// WriteGraphToIceberg writes an RDF graph into the configured Iceberg triples
// table: the graph's blank nodes are stabilized, its triples spooled, and the
// spool committed with WriteSpoolToIceberg.
func WriteGraphToIceberg(ctx context.Context, graph *rdflibgo.Graph, cfg *LoadConfig, customMetadata map[string]string, message string) error {
	if graph == nil {
		return fmt.Errorf("load graph: missing graph")
	}
	if cfg == nil {
		return fmt.Errorf("load graph: missing arguments")
	}
	spool, err := spoolGraph(graph, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := spool.Remove(); err != nil {
			slog.Warn("failed to remove the spool", "path", spool.Dir(), "error", err)
		}
	}()
	return WriteSpoolToIceberg(ctx, spool, cfg, customMetadata, message)
}

// spoolGraph spools a graph, its blank nodes stabilized, in the warehouse's
// scratch directory.
func spoolGraph(graph *rdflibgo.Graph, cfg *LoadConfig) (*Spool, error) {
	spool, err := NewSpool(filepath.Join(cfg.Warehouse, ScratchDir))
	if err != nil {
		return nil, err
	}
	if err := spool.AddGraph(StabilizeBlankNodes(graph)); err != nil {
		return nil, errors.Join(err, spool.Remove())
	}
	return spool, nil
}

// WriteSpoolToIceberg commits the rows of a spool as the next snapshot of the
// main branch of the configured triples table: every row the table does not
// hold yet is added, once, and every row of the table the spool does not hold
// is removed with an equality delete, so the snapshot holds exactly what the
// spool does. The diff is a pair of anti joins DuckDB runs over Parquet files
// (see spoolDiff), so a build's memory does not grow with the table or with
// what it writes. customMetadata is set as table properties, and message,
// when not empty, is recorded in the summary of the snapshot under
// pkg.SnapshotMessageProperty, the way a git commit carries its message. A
// write that changes nothing commits no snapshot, so it records no message.
func WriteSpoolToIceberg(ctx context.Context, spool *Spool, cfg *LoadConfig, customMetadata map[string]string, message string) (err error) {
	if spool == nil || cfg == nil {
		return fmt.Errorf("load graph: missing arguments")
	}
	ctx, span := telemetry.Start(ctx, "iceberg.write", attribute.String("sal.iceberg.warehouse", cfg.Warehouse), attribute.String("sal.iceberg.namespace", cfg.Namespace))
	defer func() { telemetry.End(span, err) }()

	tbl, cat, err := openTable(ctx, cfg)
	if err != nil {
		return err
	}
	if err := spool.Flush(); err != nil {
		return err
	}
	diff, err := openSpoolDiff(ctx, spool, tbl)
	if err != nil {
		return err
	}
	defer func() { err = joinClose(err, diff.Close()) }()

	change, err := diff.diff(ctx, tbl, spool, 0, spool.Segment(), diff.existing, 0)
	if err != nil {
		return err
	}
	if change.empty() {
		warnNoChanges(message)
	} else if _, err := commitGraphDelta(ctx, tbl, table.MainBranch, change.dataFiles, change.added, change.removedPath, change.removed, snapshotProperties(message)); err != nil {
		return err
	}
	return finishWrite(ctx, cat, tbl.Identifier(), customMetadata)
}

// openTable opens the configured triples table, creating it when there is
// none, with the build's write properties applied.
func openTable(ctx context.Context, cfg *LoadConfig) (*table.Table, catalog.Catalog, error) {
	_, tableSchema, err := GetSchemas()
	if err != nil {
		return nil, nil, err
	}
	cat, err := hadoop.NewCatalog(ctx, "local-catalog", cfg.Warehouse, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create catalog: %w", err)
	}
	tbl, err := NewIcebergTableFromCfg(ctx, tableSchema, cat, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Iceberg table: %w", err)
	}
	if err := applyWriteProperties(ctx, tbl, cfg); err != nil {
		return nil, nil, err
	}
	if tbl, err = cat.LoadTable(ctx, tbl.Identifier()); err != nil {
		return nil, nil, fmt.Errorf("load table: %w", err)
	}
	return tbl, cat, nil
}

// finishWrite sets the build's properties on the table and tags its current
// snapshot with the commit HEAD is at, which a build does whether or not it
// committed a snapshot.
func finishWrite(ctx context.Context, cat catalog.Catalog, ident table.Identifier, customMetadata map[string]string) error {
	tbl, err := cat.LoadTable(ctx, ident)
	if err != nil {
		return fmt.Errorf("reload table before setting metadata: %w", err)
	}
	tx := tbl.NewTransaction()
	if err := tx.SetProperties(customMetadata); err != nil {
		return err
	}
	if _, err := tx.Commit(ctx); err != nil {
		return err
	}
	return pkg.SetTagOfLatestSnapshot(tbl, cat)
}

// warnNoChanges says that a write found nothing to commit.
func warnNoChanges(message string) {
	if message != "" {
		slog.Warn("No changes from last Iceberg snapshot. No new snapshot will be created, so the build message is not recorded", "message", message)
	} else {
		slog.Warn("No changes from last Iceberg snapshot. No new snapshot will be created")
	}
}

// snapshotProperties is what a build's message becomes in the summary of the
// snapshot it commits: nothing when there is no message.
func snapshotProperties(message string) iceberg.Properties {
	if message == "" {
		return nil
	}
	return iceberg.Properties{pkg.SnapshotMessageProperty: message}
}

// tableChange is what a diff found and wrote: the data files of the rows to
// add and a file of the pairs to delete, each with its count, plus how many
// rows of the table stay as they are.
type tableChange struct {
	dataFiles   []iceberg.DataFile
	added       int64
	removedPath string
	removed     int64
	unchanged   int64
}

func (c tableChange) empty() bool { return c.added == 0 && c.removed == 0 }

// diff writes the data files of every row of the spool's segments from
// through to that neither the table nor an earlier diff of this spoolDiff
// added, and a file of the pairs of every row of removedFrom, a file of
// (triple_hash, predicate) pairs, that those segments do not hold, "" for
// none. The pairs to remove are found first, over the hashes of every
// spooled row; the rows to add are then found and written a partition at a
// time, and each partition's spool files are dropped once its data files are
// written, so only one partition is ever sorted and the disk the spool takes
// shrinks as the diff goes. It is one span, the one the progress pane and the
// telemetry read the counts from, so the rows it reports added are the ones
// written, each triple once; staged is added to that count for a commit that
// also carries rows staging wrote.
func (d *spoolDiff) diff(ctx context.Context, tbl *table.Table, spool *Spool, from, to int, removedFrom string, staged int64) (_ tableChange, err error) {
	ctx, span := telemetry.Start(ctx, "iceberg.diff")
	defer func() { telemetry.End(span, err) }()
	candidates := spool.Files(from, to)
	partitions := spool.Partitions(from, to)
	slog.Info("Comparing the build against the Iceberg table", "spool_files", len(candidates), "partitions", len(partitions), "table_rows", d.existingRows)

	var change tableChange
	if change.removedPath, change.removed, err = d.removed(ctx, removedFrom, candidates); err != nil {
		return change, err
	}
	exclude := slices.Clone(d.addedHashes)
	for _, partition := range partitions {
		addedPath, addedRows, err := d.added(ctx, partition, exclude)
		if err != nil {
			return change, err
		}
		if addedRows > 0 {
			// a later diff of the same build, a staged build's commit, must
			// not add these again once their spool files are gone
			hashes := d.next("added-hashes")
			if _, err := d.copy(ctx, "COPY (SELECT triple_hash FROM read_parquet("+sqlString(addedPath)+")) TO "+sqlString(hashes)+" (FORMAT parquet)"); err != nil {
				return change, err
			}
			d.addedHashes = append(d.addedHashes, hashes)
			// what DuckDB held is handed back before the data files are written
			if err := d.closeDB(); err != nil {
				return change, err
			}
			dataFiles, rows, err := writeRowFiles(ctx, tbl, addedPath)
			if err != nil {
				return change, err
			}
			change.dataFiles = append(change.dataFiles, dataFiles...)
			change.added += rows
		}
		if err := errors.Join(os.Remove(addedPath), spool.Drop(partition.Files)); err != nil && !os.IsNotExist(err) {
			return change, err
		}
	}
	if err := d.closeDB(); err != nil {
		return change, err
	}
	change.unchanged = d.existingRows - change.removed
	span.SetAttributes(
		attribute.Int64("sal.triples.existing", d.existingRows),
		attribute.Int64("sal.triples.added", staged+change.added),
		attribute.Int64("sal.triples.removed", change.removed),
		attribute.Int64("sal.triples.unchanged", change.unchanged),
	)
	slog.Info("Applying Iceberg triple diff", "added", staged+change.added, "removed", change.removed, "unchanged", change.unchanged)
	return change, nil
}

// commitGraphDelta commits appended data files and the equality deletes for
// the pairs in removedPath in one Iceberg snapshot on a branch of the table,
// with snapshotProps in the snapshot's summary, and returns the table as
// committed. added and removed are the triples the commit adds and removes,
// which the summary records.
func commitGraphDelta(ctx context.Context, tbl *table.Table, branch string, dataFiles []iceberg.DataFile, added int64, removedPath string, removed int64, snapshotProps iceberg.Properties) (_ *table.Table, err error) {
	if len(dataFiles) == 0 && removed == 0 {
		return nil, fmt.Errorf("no triples found")
	}
	ctx, span := telemetry.Start(ctx, "iceberg.commit", attribute.Int("sal.iceberg.data_files", len(dataFiles)), attribute.Int64("sal.triples.added", added), attribute.Int64("sal.triples.removed", removed))
	defer func() { telemetry.End(span, err) }()

	// the counts go into the summary with the message, so the snapshot says
	// what it changed in triples without reading its files
	snapshotProps = maps.Clone(snapshotProps)
	if snapshotProps == nil {
		snapshotProps = iceberg.Properties{}
	}
	snapshotProps[pkg.SnapshotTriplesAddedProperty] = strconv.FormatInt(added, 10)
	snapshotProps[pkg.SnapshotTriplesRemovedProperty] = strconv.FormatInt(removed, 10)

	txn, err := tbl.NewTransactionOnBranch(branch)
	if err != nil {
		return nil, err
	}
	var deleteFiles []iceberg.DataFile
	if removed > 0 {
		deleteFiles, err = writeTripleHashDeletes(ctx, txn, tbl.Schema(), removedPath)
		if err != nil {
			return nil, err
		}
	}

	rowDelta := txn.NewRowDelta(snapshotProps)
	rowDelta.AddRows(dataFiles...)
	rowDelta.AddDeletes(deleteFiles...)
	if err := rowDelta.Commit(ctx); err != nil {
		return nil, fmt.Errorf("stage row delta: %w", err)
	}

	committed, err := txn.Commit(ctx)
	if err != nil {
		return nil, fmt.Errorf("commit row delta: %w", err)
	}
	slog.Info("Successfully committed iceberg row delta", "branch", branch, "added", added, "removed", removed, "data_files", len(dataFiles), "delete_files", len(deleteFiles))
	return committed, nil
}

// writeTripleHashDeletes writes the equality deletes for the (triple_hash,
// predicate) pairs of a Parquet file the diff wrote, streamed rather than
// collected since a build may remove millions of rows.
func writeTripleHashDeletes(ctx context.Context, txn *table.Transaction, schema *iceberg.Schema, path string) (_ []iceberg.DataFile, err error) {
	tripleHashField, ok := schema.FindFieldByName("triple_hash")
	if !ok {
		return nil, fmt.Errorf("triple_hash field not found in table schema")
	}
	var rows int64
	records, closeRecords, err := readParquetRecords(ctx, path, deleteSchema(), &rows)
	if err != nil {
		return nil, err
	}
	defer func() { err = joinClose(err, closeRecords()) }()
	deleteFiles, err := txn.WriteEqualityDeletes(ctx, []int{tripleHashField.ID}, records)
	if err != nil {
		return nil, fmt.Errorf("write equality deletes: %w", err)
	}
	return deleteFiles, nil
}
