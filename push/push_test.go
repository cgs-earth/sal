package push

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPushReturnsErrorBeforeStartingUploadsWhenDataDirIsEmpty(t *testing.T) {
	err := push(context.Background(), t.TempDir(), nil, "example.com/repo")

	require.Error(t, err)
	require.Contains(t, err.Error(), "no files found in SAL data directory")
}
