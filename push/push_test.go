package push

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPushReturnsErrorBeforeStartingUploadsWhenDataDirIsEmpty(t *testing.T) {
	err := push(t.TempDir(), nil, "example.com/repo")

	require.Error(t, err)
	require.Contains(t, err.Error(), "no files found in SAL data directory")
}
