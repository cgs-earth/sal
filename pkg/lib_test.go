package pkg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindSALProjectDirFindsNearestProjectDirBelowHome(t *testing.T) {
	home := testHome(t)
	project := filepath.Join(home, "work", "project")
	nested := filepath.Join(project, "data", "rdf")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sal"), 0755))
	require.NoError(t, os.MkdirAll(nested, 0755))
	chdir(t, nested)

	got, err := SALProjectDir(func() (string, error) {
		return home, nil
	})

	require.NoError(t, err)
	want, err := canonicalPath(project)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestFindSALProjectDirFailsForHomeSALDir(t *testing.T) {
	home := testHome(t)
	nested := filepath.Join(home, "work", "project")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".sal"), 0755))
	require.NoError(t, os.MkdirAll(nested, 0755))
	chdir(t, nested)

	got, err := SALProjectDir(func() (string, error) {
		return home, nil
	})

	require.Empty(t, got)
	require.True(t, errors.Is(err, ErrSalDirNotFound), "error = %v", err)
}

func TestFindSALProjectDirDoesNotSearchAboveHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	nested := filepath.Join(home, "work", "project")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sal"), 0755))
	require.NoError(t, os.MkdirAll(nested, 0755))
	chdir(t, nested)

	got, err := SALProjectDir(func() (string, error) {
		return home, nil
	})

	require.Empty(t, got)
	require.True(t, errors.Is(err, ErrSalDirNotFound), "error = %v", err)
}

func testHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.MkdirAll(home, 0755))
	return home
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(previous))
	})
}

func TestNormalizeGitRemoteMapsScpLikeSshRemoteToHttps(t *testing.T) {
	got, err := normalizeGitRemote("git@github.com:bob/foo.git")

	require.NoError(t, err)
	require.Equal(t, "https://github.com/bob/foo.git", got)
}

func TestNormalizeGitRemoteMapsSshSchemeRemoteToHttps(t *testing.T) {
	got, err := normalizeGitRemote("ssh://git@github.com/bob/foo.git")

	require.NoError(t, err)
	require.Equal(t, "https://github.com/bob/foo.git", got)
}

func TestNormalizeGitRemoteDropsSshPortWhenMapping(t *testing.T) {
	got, err := normalizeGitRemote("ssh://git@github.com:22/bob/foo.git")

	require.NoError(t, err)
	require.Equal(t, "https://github.com/bob/foo.git", got)
}

func TestNormalizeGitRemoteLeavesHttpsRemoteUnchanged(t *testing.T) {
	got, err := normalizeGitRemote("https://github.com/bob/foo.git")

	require.NoError(t, err)
	require.Equal(t, "https://github.com/bob/foo.git", got)
}

func TestNormalizeGitRemoteLeavesNonSshRemoteUnchanged(t *testing.T) {
	got, err := normalizeGitRemote("/srv/git/foo.git")

	require.NoError(t, err)
	require.Equal(t, "/srv/git/foo.git", got)
}

func TestNormalizeGitRemoteErrorsOnUnmappableSshRemote(t *testing.T) {
	_, err := normalizeGitRemote("ssh://git@github.com")

	require.ErrorContains(t, err, "cannot be mapped to an https URL")
}

func TestFormatUploadedSizeUsesKBForSmallUploads(t *testing.T) {
	got := BytesToHumanReadable(512 * 1024)

	require.Equal(t, "512.00 KB", got)
}

func TestFormatUploadedSizeUsesMBForLargeUploads(t *testing.T) {
	got := BytesToHumanReadable(2 * 1024 * 1024)

	require.Equal(t, "2.00 MB", got)
}
