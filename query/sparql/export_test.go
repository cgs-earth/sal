package sparql

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExportSQLForSimpleObjectsSelectsTheBareObjectColumn(t *testing.T) {
	sql := ExportSQL(SimpleObjects)

	require.Contains(t, sql, "SELECT subject, predicate, object FROM triples")
	require.NotContains(t, sql, "triple_hash")
}

func TestExportSQLForTypedObjectsSelectsEveryUnionColumn(t *testing.T) {
	sql := ExportSQL(TypedObjects)

	require.Contains(t, sql, "subject")
	require.Contains(t, sql, "predicate")
	require.Contains(t, sql, "object_iri")
	require.Contains(t, sql, "CAST(object_float AS VARCHAR) AS object_float")
	require.Contains(t, sql, "ST_AsText(object_geometry) AS object_wkt")
	require.Contains(t, sql, "object_string")
	require.NotContains(t, sql, "triple_hash")
}

func TestStreamRowsCallsRowFnForEveryRow(t *testing.T) {
	var got [][]string
	err := streamRows(context.Background(), localDB(t), "SELECT * FROM (VALUES (1, 'a'), (2, 'b')) t(i, s)", func(row []sql.NullString) error {
		got = append(got, []string{row[0].String, row[1].String})
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, [][]string{{"1", "a"}, {"2", "b"}}, got)
}

func TestStreamRowsPreservesNullRatherThanCollapsingItToEmptyString(t *testing.T) {
	var got []sql.NullString
	err := streamRows(context.Background(), localDB(t), "SELECT NULL AS a, '' AS b", func(row []sql.NullString) error {
		got = append(got, row...)
		return nil
	})

	require.NoError(t, err)
	require.False(t, got[0].Valid, "a real SQL NULL should scan as invalid")
	require.True(t, got[1].Valid, "an empty string is a value, not a NULL")
	require.Equal(t, "", got[1].String)
}

func TestStreamRowsStopsAndReturnsTheErrorRowFnReturns(t *testing.T) {
	calls := 0
	stop := require.New(t)
	err := streamRows(context.Background(), localDB(t), "SELECT i FROM range(10) t(i)", func(row []sql.NullString) error {
		calls++
		if calls == 3 {
			return context.Canceled
		}
		return nil
	})

	stop.ErrorIs(err, context.Canceled)
	stop.Equal(3, calls)
}

func TestStreamRowsReportsAStatementThatDoesNotParse(t *testing.T) {
	err := streamRows(context.Background(), localDB(t), "SELCT 1", func(row []sql.NullString) error { return nil })

	require.ErrorContains(t, err, "duckdb query failed")
}
