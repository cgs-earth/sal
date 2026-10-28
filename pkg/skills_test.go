package pkg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/pkg/skillstest"
	"github.com/stretchr/testify/require"
)

func TestInstallSkillsCopiesEachSkillIntoEveryAgentDirectory(t *testing.T) {
	skillstest.NewRepository(t, "sal", "salmodule")
	dir := t.TempDir()

	require.NoError(t, pkg.InstallSkills(dir, "sal"))

	for _, skillDir := range []string{".claude", ".agents"} {
		content, err := os.ReadFile(filepath.Join(dir, skillDir, "skills", "sal", "SKILL.md"))
		require.NoError(t, err)
		require.Equal(t, "---\nname: sal\n---\n# sal\n", string(content))
		require.FileExists(t, filepath.Join(dir, skillDir, "skills", "sal", "references", "notes.md"))
		require.NoDirExists(t, filepath.Join(dir, skillDir, "skills", "salmodule"), "only the skill asked for is installed")
	}
	require.NoFileExists(t, filepath.Join(dir, "readme.md"))
	require.Equal(t, []string{filepath.Join(".claude", "skills", "sal", "SKILL.md"), filepath.Join(".agents", "skills", "sal", "SKILL.md")}, pkg.InstalledSkills(dir, "sal"))
	require.Empty(t, pkg.InstalledSkills(dir, "salmodule"))
}

func TestInstallSkillsReplacesAnEarlierCopy(t *testing.T) {
	skillstest.NewRepository(t, "sal")
	dir := t.TempDir()
	stale := filepath.Join(dir, ".claude", "skills", "sal")
	require.NoError(t, os.MkdirAll(stale, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "SKILL.md"), []byte("old\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "stale.md"), []byte("gone\n"), 0644))

	require.NoError(t, pkg.InstallSkills(dir, "sal"))

	content, err := os.ReadFile(filepath.Join(stale, "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, "---\nname: sal\n---\n# sal\n", string(content))
	require.NoFileExists(t, filepath.Join(stale, "stale.md"))
}

func TestInstallSkillsReportsASkillTheRepositoryLacks(t *testing.T) {
	skillstest.NewRepository(t, "sal")
	require.ErrorContains(t, pkg.InstallSkills(t.TempDir(), "nonesuch"), `has no skill named "nonesuch"`)
}

func TestInstallSkillsReportsARepositoryThatCannotBeCloned(t *testing.T) {
	previous := pkg.SkillsRepository
	pkg.SkillsRepository = "file://" + filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { pkg.SkillsRepository = previous })
	require.ErrorContains(t, pkg.InstallSkills(t.TempDir(), "sal"), "download the agent skills from")
}
