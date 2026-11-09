//go:build !windows

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-157: the copies of Codex's skills go to ~/.agents/skills, the folder Codex's
// documentation names for the user's skills. ~/.codex/skills is deprecated, and still
// read: it is where the install used to put them. What says "Codex is on this machine"
// is still ~/.codex, its configuration folder, which is not the folder its copies go to.
//
// The tests of makefile_skills_test.go and makefile_skills_shared_test.go that fix where
// the Codex copy is were changed to the new path and nothing else; these are the ones the
// move itself needs. Same rules as there: a fixture of skills and a HOME inside
// t.TempDir(), never the real home, never the repo's own skills.

// dirNames is the names directly under dir, sorted.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, dir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// agentsSkills is Codex's folder for the user's skills in the fixture's HOME.
func (fx skillsFixture) agentsSkills(path ...string) string {
	return filepath.Join(append([]string{fx.home, ".agents", "skills"}, path...)...)
}

func TestMakefile_SkillsForCodexGoToAgentsSkills(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	install := func(t *testing.T, fx skillsFixture, extra ...string) string {
		t.Helper()
		out, code := fx.make(t, repoRoot, "install-skills", extra...)
		require.Equal(t, 0, code, out)
		return out
	}

	// Criterion 1. The two kinds of skill Codex gets: its own and the shared ones.
	t.Run("a Codex skill and a shared one go to ~/.agents/skills, and ~/.codex gets nothing", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		codex := filepath.Join(fx.home, ".codex")
		require.Empty(t, dirNames(t, codex), "premise: ~/.codex is there, and empty")
		require.NoDirExists(t, filepath.Join(fx.home, ".agents"), "premise: no ~/.agents yet")

		out := install(t, fx)

		assert.Equal(t, "alpha for codex\n", readFile(t, fx.agentsSkills("alpha", "SKILL.md")))
		want := relTree(t, filepath.Join(fx.skills, "shared", "delta"))
		require.Len(t, want, 2)
		assert.Equal(t, want, relTree(t, fx.agentsSkills("delta")), "the shared skill, with the source's content")
		assert.Contains(t, out, "installed  codex/alpha -> "+fx.agentsSkills("alpha"))
		assert.Contains(t, out, "installed  codex/delta -> "+fx.agentsSkills("delta"))

		assert.Empty(t, dirNames(t, codex), "~/.codex is as it was: not even a skills folder")
		assert.Equal(t, []string{"skills"}, dirNames(t, filepath.Join(fx.home, ".agents")))
		assert.Equal(t, []string{"alpha", "delta"}, dirNames(t, fx.agentsSkills()), "and only the repo's skills are in it")
		assert.Equal(t, "alpha for claude\n", readFile(t, fx.copyOf(".claude", "alpha", "SKILL.md")), "Claude Code's copy is where it was")
	})

	// Criterion 2, the branch where the folder is not there: it is made by install, and
	// a check makes nothing.
	t.Run("with ~/.codex and without ~/.agents the folder is created by install, not by check", func(t *testing.T) {
		fx := newSkillsFixture(t)
		require.NoDirExists(t, filepath.Join(fx.home, ".agents"), "premise")

		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "missing    codex/alpha  ("+fx.agentsSkills("alpha")+")")
		assert.NoDirExists(t, filepath.Join(fx.home, ".agents"), "a check creates nothing")

		install(t, fx)
		assert.DirExists(t, fx.agentsSkills("alpha"))
		out, code = fx.make(t, repoRoot, "skills-check")
		assert.Equal(t, 0, code, out)
		assert.Contains(t, out, "ok         codex/alpha")
	})

	// Criterion 2, the other branch: ~/.agents is not what says Codex is here.
	t.Run("without ~/.codex the Codex copies are skipped even if ~/.agents exists, and ~/.agents is left alone", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		require.NoError(t, os.RemoveAll(filepath.Join(fx.home, ".codex")))
		write(t, fx.agentsSkills("personal", "SKILL.md"), "the user's own, for Codex\n")
		require.DirExists(t, fx.agentsSkills("personal"), "premise: ~/.agents/skills is there")
		before := tree(t, filepath.Join(fx.home, ".agents"))

		out := install(t, fx)

		assert.Contains(t, out, "skipped    codex/alpha")
		assert.Contains(t, out, "skipped    codex/delta")
		assert.Contains(t, out, "installed  claude/alpha", "the runtime that is here is installed")
		assert.NotContains(t, out, "personal", "the user's own skill is not named")
		assert.Equal(t, before, tree(t, filepath.Join(fx.home, ".agents")), "~/.agents is as it was")
		assert.NoDirExists(t, filepath.Join(fx.home, ".codex"), "and ~/.codex is not created")

		checked, code := fx.make(t, repoRoot, "skills-check")
		assert.Equal(t, 0, code, checked)
		assert.Contains(t, checked, "skipped    codex/alpha")
	})

	// Criterion 4, on the new path: the promises are the script's, not the folder's.
	t.Run("~/.agents/skills being a link is the user's arrangement and works", func(t *testing.T) {
		fx := newSkillsFixture(t)
		dotfiles := filepath.Join(t.TempDir(), "dotfiles", "agents skills")
		write(t, filepath.Join(dotfiles, "personal", "SKILL.md"), "the user's own\n")
		require.NoError(t, os.MkdirAll(filepath.Join(fx.home, ".agents"), 0o700))
		require.NoError(t, os.Symlink(dotfiles, fx.agentsSkills()))

		out := install(t, fx)

		assert.Contains(t, out, "installed  codex/alpha")
		assert.Equal(t, "alpha for codex\n", readFile(t, filepath.Join(dotfiles, "alpha", "SKILL.md")), "under the link, in the skill's own folder")
		assert.Equal(t, "the user's own\n", readFile(t, filepath.Join(dotfiles, "personal", "SKILL.md")))
		assert.Contains(t, install(t, fx), "unchanged  codex/alpha")
		out, code := fx.make(t, repoRoot, "skills-check")
		assert.Equal(t, 0, code, out)
	})

	t.Run("a skill folder in ~/.agents/skills that is a symbolic link is refused, with or without FORCE", func(t *testing.T) {
		fx := newSkillsFixture(t)
		elsewhere := filepath.Join(t.TempDir(), "somewhere else")
		write(t, filepath.Join(elsewhere, "keep.md"), "not the install's to touch\n")
		write(t, filepath.Join(elsewhere, "SKILL.md"), "not the install's either\n")
		before := tree(t, elsewhere)
		link := fx.agentsSkills("alpha")
		require.NoError(t, os.MkdirAll(fx.agentsSkills(), 0o700))
		require.NoError(t, os.Symlink(elsewhere, link))

		for _, extra := range [][]string{nil, {"FORCE=1"}} {
			out, code := fx.make(t, repoRoot, "install-skills", extra...)
			assert.NotEqual(t, 0, code, "%v: %s", extra, out)
			assert.Contains(t, out, "refused", "%v", extra)
			assert.Contains(t, out, link, "the path of the link is named: %v", extra)
			assert.Equal(t, before, tree(t, elsewhere), "nothing was written through it: %v", extra)
			assert.NoDirExists(t, fx.copyOf(".claude", "alpha"), "all or nothing: %v", extra)
		}
		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "linked     codex/alpha")
		assert.Equal(t, before, tree(t, elsewhere), "a check does not read through it either")
	})

	t.Run("a dangling link where the Codex copy should be is refused, not created through", func(t *testing.T) {
		fx := newSkillsFixture(t)
		gone := filepath.Join(t.TempDir(), "not there")
		link := fx.agentsSkills("alpha")
		require.NoError(t, os.MkdirAll(fx.agentsSkills(), 0o700))
		require.NoError(t, os.Symlink(gone, link))

		out, code := fx.make(t, repoRoot, "install-skills", "FORCE=1")

		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "symbolic link")
		assert.Contains(t, out, link)
		assert.NoDirExists(t, gone, "the target of the link was not created")
	})
}
