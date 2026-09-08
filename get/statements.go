package get

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	salsparql "github.com/cgs-earth/sal/query/sparql"
)

// statementsBatchSize is how many rows are aligned and written together. A
// listing streams off DuckDB rather than being buffered whole, so a table
// larger than memory can still be listed; the columns are aligned within each
// batch, since aligning across all of them would mean holding all of them.
const statementsBatchSize = 5000

type statementsCmd struct {
	Limit int `arg:"--limit" help:"the maximum number of statements to list; 0 lists every statement"`
}

// Run streams the statements of the data product to standard out as a table
// with one row per triple: the subject, the predicate, the object rendered as
// text whichever typed column holds it, and the datatype and language tag of
// a literal object, which are empty for an IRI or blank node object.
func (cmd *statementsCmd) Run() error {
	if cmd.Limit < 0 {
		return fmt.Errorf("--limit must not be negative")
	}
	ctx := context.Background()
	table, err := salsparql.LocateTriplesTable()
	if err != nil {
		return err
	}
	runner, err := table.Runner(ctx, 0)
	if err != nil {
		return err
	}

	out := bufio.NewWriter(os.Stdout)
	writer := newStatementsWriter(out)
	// a geometry object is rendered to WKT by ST_AsText, which needs spatial loaded
	if err := runner.StreamSQL(ctx, salsparql.StatementsSQL(cmd.Limit), true, writer.write); err != nil {
		return err
	}
	if err := writer.flush(); err != nil {
		return err
	}
	if writer.rows == 0 {
		fmt.Println("no statements found; the data product is empty")
		return nil
	}
	return out.Flush()
}

// statementsWriter aligns streamed rows into a table, a batch at a time.
type statementsWriter struct {
	out     io.Writer
	table   *tabwriter.Writer
	pending int
	rows    int
}

func newStatementsWriter(out io.Writer) *statementsWriter {
	writer := &statementsWriter{out: out}
	writer.table = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer.table, strings.Join([]string{"subject", "predicate", "object", "type", "language"}, "\t"))
	return writer
}

// write appends one row, rendering a NULL datatype or language as an empty
// cell, and flushes the batch when it is full.
func (w *statementsWriter) write(row []sql.NullString) error {
	cells := make([]string, len(row))
	for i, cell := range row {
		cells[i] = cell.String
	}
	_, _ = fmt.Fprintln(w.table, strings.Join(cells, "\t"))
	w.pending++
	w.rows++
	if w.pending >= statementsBatchSize {
		return w.flush()
	}
	return nil
}

func (w *statementsWriter) flush() error {
	if err := w.table.Flush(); err != nil {
		return err
	}
	w.pending = 0
	return nil
}
