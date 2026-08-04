package build

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIcebergTablePropertiesRecordsDownloadedModules(t *testing.T) {
	properties, err := icebergTableProperties("abc123", []string{
		"salmodule://github.com/test/one",
		"salmodule://github.com/test/two",
	})

	require.NoError(t, err)
	require.Equal(t, "abc123", properties["sal.hash"])
	require.JSONEq(t, `["salmodule://github.com/test/one","salmodule://github.com/test/two"]`, properties["sal.salmodules"])
}

func TestIcebergTablePropertiesRecordsAnEmptyModuleList(t *testing.T) {
	properties, err := icebergTableProperties("abc123", nil)

	require.NoError(t, err)
	// null would leave a reader unable to tell an empty list from a stale value
	require.Equal(t, "[]", properties["sal.salmodules"])
}
