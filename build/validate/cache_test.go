package validate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClearCacheRemovesTempCacheRoot(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "sal", "cache")
	originalCacheRootDir := cacheRootDir
	cacheRootDir = func() string {
		return cacheDir
	}
	t.Cleanup(func() {
		cacheRootDir = originalCacheRootDir
	})

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "vocab"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "vocab", "cached.json"), []byte("{}"), 0644))

	require.NoError(t, ClearCache())

	_, err := os.Stat(cacheDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, filepath.Join(cacheDir, "vocab"), defaultVocabularyCacheDir())
}
