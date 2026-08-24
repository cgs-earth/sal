package get

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDropEmptyColumnsRemovesAnAnnotationNoDatatypeStates(t *testing.T) {
	header, rows := dropEmptyColumns(
		[]string{"datatype", "rdfs:label", "rdfs:comment"},
		[][]string{
			{"https://example.org/Celsius", "Celsius", ""},
			{"https://example.org/Knots", "", ""},
		},
	)
	require.Equal(t, []string{"datatype", "rdfs:label"}, header)
	require.Equal(t, [][]string{
		{"https://example.org/Celsius", "Celsius"},
		{"https://example.org/Knots", ""},
	}, rows)
}

func TestDropEmptyColumnsKeepsColumnsThatAnyRowStates(t *testing.T) {
	header := []string{"datatype", "rdfs:label", "rdfs:comment"}
	rows := [][]string{
		{"https://example.org/Celsius", "Celsius", "degrees Celsius"},
		{"https://example.org/Knots", "", "nautical miles per hour"},
	}
	keptHeader, keptRows := dropEmptyColumns(header, rows)
	require.Equal(t, header, keptHeader)
	require.Equal(t, rows, keptRows)
}
