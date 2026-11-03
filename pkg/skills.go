package pkg

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The agent skills the sal repository ships under skills/, which `sal init`
// offers to install into the project or module it initializes: SalSkill
// teaches an agent to work in a SAL project and SalModuleSkill to write a
// SAL module.
const (
	SalSkill       = "sal"
	SalModuleSkill = "salmodule"
)

// SkillDirs are the directories, relative to a project or module root, a
// skill is installed under: Claude Code reads .claude/skills, and the Agent
// Skills convention other agents follow reads .agents/skills. A skill is
// copied into both until the agents agree on one.
var SkillDirs = []string{filepath.Join(".claude", "skills"), filepath.Join(".agents", "skills")}

// SkillsRepository is the git repository the skills are downloaded from, the
// sal repository itself, which holds them under skills/<name>. Tests point
// it at a local repository.
var SkillsRepository = "https://github.com/cgs-earth/sal"

// skillsPath is where SkillsRepository holds the skills.
const skillsPath = "skills"

// InstalledSkills lists the SKILL.md files of the named skill that are
// installed under dir, one per SkillDirs entry that has it, relative to dir.
func InstalledSkills(dir string, skill string) []string {
	var installed []string
	for _, skillDir := range SkillDirs {
		relative := filepath.Join(skillDir, skill, "SKILL.md")
		if info, err := os.Stat(filepath.Join(dir, relative)); err == nil && !info.IsDir() {
			installed = append(installed, relative)
		}
	}
	return installed
}

// InstallSkills downloads the named skills from SkillsRepository and copies
// each into every SkillDirs directory under dir, replacing a copy already
// there so that a re-run picks up the current version. The repository is
// cloned once, shallow and sparse to its skills directory, into a temporary
// directory that is removed afterwards; git is what sal already requires, so
// no other tool is needed. The skills a sal binary installs are the ones on
// the repository's default branch, which describe the CLI as it stands.
func InstallSkills(dir string, skills ...string) error {
	if len(skills) == 0 {
		return nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is needed to download the agent skills: %w", err)
	}
	checkout, err := os.MkdirTemp("", "sal-skills-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(checkout); err != nil {
			slog.Warn("failed to remove the temporary skills checkout", "path", checkout, "error", err)
		}
	}()
	// a shallow clone without a checkout, then only skills/ checked out, so
	// the download is the skills and not the whole repository; the blob
	// filter is ignored with a warning by a server that cannot honor it
	steps := [][]string{
		{"clone", "--quiet", "--depth", "1", "--filter=blob:none", "--no-checkout", SkillsRepository, checkout},
		{"-C", checkout, "sparse-checkout", "set", "--no-cone", skillsPath},
		{"-C", checkout, "checkout", "--quiet"},
	}
	for _, step := range steps {
		cmd := exec.Command("git", step...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("download the agent skills from %s: git %s: %w\n%s", SkillsRepository, strings.Join(step, " "), err, strings.TrimSpace(string(out)))
		}
	}
	for _, skill := range skills {
		source := filepath.Join(checkout, skillsPath, skill)
		if info, err := os.Stat(source); err != nil || !info.IsDir() {
			return fmt.Errorf("%s has no skill named %q under %s/", SkillsRepository, skill, skillsPath)
		}
		for _, skillDir := range SkillDirs {
			target := filepath.Join(dir, skillDir, skill)
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			if err := copyTree(source, target); err != nil {
				return fmt.Errorf("install the %s skill into %s: %w", skill, target, err)
			}
			slog.Info("installed the " + skill + " agent skill at " + filepath.Join(skillDir, skill))
		}
	}
	return nil
}

// copyTree copies the regular files and directories under source to target,
// which must not exist yet. A skill holds only files, so anything else, a
// symbolic link say, is an error rather than silently skipped.
func copyTree(source string, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		switch {
		case entry.IsDir():
			return os.MkdirAll(destination, 0755)
		case entry.Type().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(destination, content, 0644)
		default:
			return fmt.Errorf("%s is not a regular file or directory", path)
		}
	})
}
