package sparql

import (
	"context"
	"database/sql"
	"fmt"
)

// ExportSQL selects the columns an N-Triples export needs for the table's
// object column layout, leaving out triple_hash since it identifies a row
// rather than being part of the triple it names. The typed layout's object
// union is read back as its four candidate columns; object_geometry comes
// back as WKT text since the driver has no Go value for DuckDB's GEOMETRY,
// and object_float is cast to text so its rendering matches what every other
// numeric object lookup in this package already produces.
func ExportSQL(layout ObjectLayout) string {
	if layout == SimpleObjects {
		return "SELECT subject, predicate, object FROM triples"
	}
	return `SELECT
	subject,
	predicate,
	object_iri,
	CAST(object_float AS VARCHAR) AS object_float,
	ST_AsText(object_geometry) AS object_wkt,
	object_string
FROM triples`
}

// StreamSQL runs a statement against the triples table and calls rowFn with
// each row, without buffering the result set in memory the way RunSQL does,
// so a table larger than memory can still be read in full. NULL is kept as
// NULL through sql.NullString rather than collapsed to the empty string,
// since a caller reading the typed object layout has to tell an absent union
// member apart from one holding an actual empty string.
//
// The slice passed to rowFn is reused across rows, so a caller that needs to
// keep a value beyond the call must copy it first.
func (r DuckDBRunner) StreamSQL(ctx context.Context, statement string, withSpatial bool, rowFn func([]sql.NullString) error) error {
	db, err := instance.prepare(ctx, r.TablePath, r.Imports, withSpatial)
	if err != nil {
		return err
	}
	return streamRows(ctx, db, statement, rowFn)
}

// streamRows runs a statement and calls rowFn with each row read directly off
// the driver, without the CSV-style buffering queryRows does.
func streamRows(ctx context.Context, db *sql.DB, statement string, rowFn func([]sql.NullString) error) error {
	rows, err := db.QueryContext(ctx, statement)
	if err != nil {
		return fmt.Errorf("duckdb query failed: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	header, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("duckdb query failed: %w", err)
	}
	values := make([]sql.NullString, len(header))
	targets := make([]any, len(header))
	for i := range values {
		targets[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return fmt.Errorf("read the duckdb result: %w", err)
		}
		if err := rowFn(values); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("duckdb query failed: %w", err)
	}
	return nil
}
