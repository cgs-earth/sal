package get

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func TestStatementsWriterAlignsTheColumnsUnderTheHeader(t *testing.T) {
	var out bytes.Buffer
	writer := newStatementsWriter(&out)
	require.NoError(t, writer.write([]sql.NullString{nullString("http://example.org/s"), nullString("http://www.w3.org/2000/01/rdf-schema#label"), nullString("chat"), nullString("http://www.w3.org/1999/02/22-rdf-syntax-ns#langString"), nullString("fr")}))
	require.NoError(t, writer.write([]sql.NullString{nullString("http://example.org/s"), nullString("http://example.org/p"), nullString("http://example.org/o"), {}, {}}))
	require.NoError(t, writer.flush())

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, 2, writer.rows)
	require.Equal(t, []string{"subject", "predicate", "object", "type", "language"}, strings.Fields(lines[0]))
	require.Equal(t, []string{"http://example.org/s", "http://www.w3.org/2000/01/rdf-schema#label", "chat", "http://www.w3.org/1999/02/22-rdf-syntax-ns#langString", "fr"}, strings.Fields(lines[1]))
	// a NULL datatype and language render as empty cells
	require.Equal(t, []string{"http://example.org/s", "http://example.org/p", "http://example.org/o"}, strings.Fields(lines[2]))
	// every column starts at the same offset on every line
	require.Equal(t, strings.Index(lines[0], "predicate"), strings.Index(lines[1], "http://www.w3.org/2000"))
	require.Equal(t, strings.Index(lines[0], "predicate"), strings.Index(lines[2], "http://example.org/p"))
	require.Equal(t, strings.Index(lines[0], "object"), strings.Index(lines[1], "chat"))
	require.Equal(t, strings.Index(lines[0], "object"), strings.Index(lines[2], "http://example.org/o"))
	require.Equal(t, strings.Index(lines[0], "type"), strings.Index(lines[1], "http://www.w3.org/1999"))
	require.Equal(t, strings.Index(lines[0], "language"), strings.Index(lines[1], "fr"))
}

// TestStatementsWriterFlushesEveryBatch checks that a listing is written out
// as it streams rather than only when it ends, so a large table is not held
// in memory whole.
func TestStatementsWriterFlushesEveryBatch(t *testing.T) {
	var out bytes.Buffer
	writer := newStatementsWriter(&out)
	row := []sql.NullString{nullString("s"), nullString("p"), nullString("o"), {}, {}}
	for i := 0; i < statementsBatchSize-1; i++ {
		require.NoError(t, writer.write(row))
	}
	require.Empty(t, out.String())

	require.NoError(t, writer.write(row))
	require.Equal(t, 0, writer.pending)
	require.Equal(t, statementsBatchSize, writer.rows)
	require.Equal(t, statementsBatchSize+1, strings.Count(out.String(), "\n"))
}
