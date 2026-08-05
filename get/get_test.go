package get

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatTableAlignsColumnsUnderTheirHeader(t *testing.T) {
	output := formatTable(
		[]string{"class", "instances"},
		[][]string{
			{"https://schema.org/Person", "12"},
			{"https://schema.org/Place", "3"},
		},
	)
	require.Equal(t, ""+
		"class                      instances\n"+
		"https://schema.org/Person  12\n"+
		"https://schema.org/Place   3\n", output)
}

func TestFormatTableOmitsHeaderRowWhenThereIsNoHeader(t *testing.T) {
	require.Equal(t, "https://schema.org/Person\n", formatTable(nil, [][]string{{"https://schema.org/Person"}}))
}
