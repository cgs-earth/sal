// Package skillstest holds the test support for installing agent skills: a
// local git repository holding skills that tests install from instead of the
// sal repository on GitHub.
package skillstest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/pkg"
	"github.com/stretchr/testify/require"
)

// NewRepository makes a git repository holding the named skills under
// skills/, each with a SKILL.md and a supporting file, and points
// pkg.SkillsRepository at it for the rest of the test, so that a test
// installing skills never reaches the network.
func NewRepository(t *testing.T, skills ...string) string {
	t.Helper()
	repo := t.TempDir()
	for _, skill := range skills {
		dir := filepath.Join(repo, "skills", skill)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "references"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+skill+"\n---\n# "+skill+"\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "references", "notes.md"), []byte("notes\n"), 0644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "readme.md"), []byte("not a skill\n"), 0644))
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "skills"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	previous := pkg.SkillsRepository
	pkg.SkillsRepository = "file://" + repo
	t.Cleanup(func() { pkg.SkillsRepository = previous })
	return repo
}
