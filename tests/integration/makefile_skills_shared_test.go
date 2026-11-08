//go:build !windows

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-155: a skill in skills/shared/<name>/ is one folder in the repo and two copies on the
// machine, one for Claude Code and one for Codex. These tests are about what is new in
// that: the two copies, and the refusal of a name that is in shared/ and in a runtime's
// own folder. Everything a copy must obey (the record, the links, the edits by hand) is
// the logic of makefile_skills_test.go, run here on the copy of the second runtime.
//
// Same rules as there: a fixture of skills and a HOME inside t.TempDir(), never the real
// home, never the repo's own skills. This file is separate because that one is past the
// size a file should have, and the existing tests are the proof that a runtime's own
// skills behave as before: they are not touched.

const sharedRecord = ".cab-installed"

// withSharedSkill adds a skill that lives once and goes to Claude Code and to Codex.
func (fx skillsFixture) withSharedSkill(t *testing.T) skillsFixture {
	t.Helper()
	write(t, filepath.Join(fx.skills, "shared", "delta", "SKILL.md"), "delta, for both\n")
	write(t, filepath.Join(fx.skills, "shared", "delta", "refs", "x.md"), "x\n")
	return fx
}

// copyOf is a file of the installed copy of a skill in a runtime's folder (".claude").
func (fx skillsFixture) copyOf(runtimeDir, name string, file ...string) string {
	return filepath.Join(append([]string{fx.home, runtimeDir, "skills", name}, file...)...)
}

// sharedSource is a file of the shared skill in the fixture's repo.
func (fx skillsFixture) sharedSource(file ...string) string {
	return filepath.Join(append([]string{fx.skills, "shared", "delta"}, file...)...)
}

// relTree is every file of a skill's folder with its content, by path relative to the
// folder, without the record: what two copies of the same skill must have in common.
func relTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for path, content := range tree(t, dir) {
		rel, err := filepath.Rel(dir, path)
		require.NoError(t, err)
		if rel != sharedRecord {
			out[rel] = content
		}
	}
	return out
}

func TestMakefile_SharedSkills(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	_, zipErr := exec.LookPath("zip")

	install := func(t *testing.T, fx skillsFixture, extra ...string) string {
		t.Helper()
		out, code := fx.make(t, repoRoot, "install-skills", extra...)
		require.Equal(t, 0, code, out)
		return out
	}
	bothRuntimes := []string{".claude", ".codex"}

	t.Run("a shared skill is installed in Claude Code and in Codex with the source's content", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)

		out := install(t, fx)

		want := relTree(t, filepath.Join(fx.skills, "shared", "delta"))
		require.Len(t, want, 2)
		for _, dir := range bothRuntimes {
			assert.Equal(t, want, relTree(t, fx.copyOf(dir, "delta")), dir)
		}
		assert.Contains(t, out, "installed  claude/delta -> "+fx.copyOf(".claude", "delta"))
		assert.Contains(t, out, "installed  codex/delta -> "+fx.copyOf(".codex", "delta"))
		assert.Equal(t, "alpha for claude\n", readFile(t, fx.copyOf(".claude", "alpha", "SKILL.md")), "the runtime's own skills go where they went")
		assert.Equal(t, "alpha for codex\n", readFile(t, fx.copyOf(".codex", "alpha", "SKILL.md")))
	})

	t.Run("a shared skill does not go to Antigravity or to Claude Desktop", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		require.NoError(t, os.MkdirAll(filepath.Join(fx.home, ".gemini", "config"), 0o700))
		if zipErr == nil {
			fx = fx.withDesktopSkill(t)
		}

		install(t, fx)

		assert.NoDirExists(t, filepath.Join(fx.home, ".gemini", "config", "skills", "delta"))
		assert.NoFileExists(t, filepath.Join(fx.bin, "delta.zip"))
		assert.DirExists(t, fx.copyOf(".claude", "delta"), "while the two it is for have it")
	})

	t.Run("check says one line for each copy, and exits 1 when either is not ok", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		install(t, fx)

		out, code := fx.script(t, repoRoot, "check")
		require.Equal(t, 0, code, out)
		assert.Equal(t, 1, strings.Count(out, "ok         claude/delta"), out)
		assert.Equal(t, 1, strings.Count(out, "ok         codex/delta"), out)

		write(t, fx.copyOf(".codex", "delta", "refs", "x.md"), "changed\n")
		out, code = fx.script(t, repoRoot, "check")
		assert.Equal(t, 1, code, "the script itself, make says 2: "+out)
		assert.Contains(t, out, "ok         claude/delta", "the copy that is fine is still said")
		assert.Contains(t, out, "different  codex/delta")
		assert.Contains(t, out, "differs: refs/x.md")
		_, viaMake := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, viaMake)

		install(t, fx, "FORCE=1")
		require.NoError(t, os.RemoveAll(fx.copyOf(".claude", "delta")))
		out, code = fx.script(t, repoRoot, "check")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "missing    claude/delta")
		assert.Contains(t, out, "ok         codex/delta")
	})

	t.Run("without ~/.codex that copy is skipped and the other is installed", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		require.NoError(t, os.RemoveAll(filepath.Join(fx.home, ".codex")))

		out := install(t, fx)

		assert.Contains(t, out, "skipped    codex/delta")
		assert.Contains(t, out, "installed  claude/delta")
		assert.Equal(t, "delta, for both\n", readFile(t, fx.copyOf(".claude", "delta", "SKILL.md")))
		assert.NoDirExists(t, filepath.Join(fx.home, ".codex"), "and not created")

		checked, code := fx.script(t, repoRoot, "check")
		assert.Equal(t, 0, code, checked)
		assert.Contains(t, checked, "skipped    codex/delta")
		assert.Contains(t, checked, "ok         claude/delta")
	})

	// What a copy must obey, on the Codex copy of a shared skill.
	t.Run("a Codex copy that differs is refused, named, and the Claude copy is not installed either", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		install(t, fx)
		write(t, fx.copyOf(".codex", "delta", "SKILL.md"), "delta, for both\nA LOCAL EDIT\n")
		require.NoError(t, os.RemoveAll(fx.copyOf(".claude", "delta")))
		before := fx.runtimeTree(t)

		out, code := fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "refused")
		assert.Contains(t, out, "codex/delta")
		assert.Contains(t, out, "differs: SKILL.md")
		assert.Equal(t, "delta, for both\nA LOCAL EDIT\n", readFile(t, fx.copyOf(".codex", "delta", "SKILL.md")), "the local edit is still there")
		assert.NoDirExists(t, fx.copyOf(".claude", "delta"), "all or nothing")
		assert.Equal(t, before, fx.runtimeTree(t), "the Claude copy was not even begun: every file of every runtime folder is as it was")

		out = install(t, fx, "FORCE=1")
		assert.Contains(t, out, "overwrote  codex/delta: SKILL.md", "FORCE says what it overwrote")
		assert.Equal(t, "delta, for both\n", readFile(t, fx.copyOf(".codex", "delta", "SKILL.md")))
		assert.Equal(t, "delta, for both\n", readFile(t, fx.copyOf(".claude", "delta", "SKILL.md")), "and the missing one is installed")
	})

	t.Run("both copies get a record, and a copy that is only behind is updated without FORCE", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		install(t, fx)
		for _, dir := range bothRuntimes {
			assert.FileExists(t, fx.copyOf(dir, "delta", sharedRecord), dir)
		}

		write(t, fx.sharedSource("SKILL.md"), "delta, newer\n")
		out := install(t, fx)

		assert.Contains(t, out, "updated    claude/delta: SKILL.md")
		assert.Contains(t, out, "updated    codex/delta: SKILL.md")
		assert.Equal(t, "delta, newer\n", readFile(t, fx.copyOf(".codex", "delta", "SKILL.md")))
	})

	t.Run("a symbolic link inside the Codex copy is refused with or without FORCE, and nothing is copied", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		install(t, fx)
		elsewhere := filepath.Join(t.TempDir(), "x.md")
		write(t, elsewhere, "outside\n")
		link := fx.copyOf(".codex", "delta", "refs", "x.md")
		require.NoError(t, os.Remove(link))
		require.NoError(t, os.Symlink(elsewhere, link))
		write(t, fx.sharedSource("SKILL.md"), "delta, newer\n") // the Claude copy would be updated

		for _, extra := range [][]string{nil, {"FORCE=1"}} {
			out, code := fx.make(t, repoRoot, "install-skills", extra...)
			assert.NotEqual(t, 0, code, out)
			assert.Contains(t, out, "refused")
			assert.Contains(t, out, link, "the link is named")
			assert.Equal(t, "outside\n", readFile(t, elsewhere), "nothing was written through it")
			assert.Equal(t, "delta, for both\n", readFile(t, fx.copyOf(".claude", "delta", "SKILL.md")), "all or nothing")
		}
	})

	t.Run("files added by hand in the Codex copy: kept, unless the repo acquires one of them", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		install(t, fx)
		write(t, fx.copyOf(".codex", "delta", "mine.md"), "only mine\n")
		write(t, fx.sharedSource("SKILL.md"), "delta, newer\n")

		out := install(t, fx)
		assert.Contains(t, out, "updated    codex/delta: SKILL.md", "only behind: no FORCE")
		assert.Contains(t, out, "only in the installed copy, left alone: mine.md")
		assert.Equal(t, "only mine\n", readFile(t, fx.copyOf(".codex", "delta", "mine.md")))

		write(t, fx.sharedSource("mine.md"), "the repo's\n")
		out, code := fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "added by hand, and the repo now has another one: mine.md")
		assert.Equal(t, "only mine\n", readFile(t, fx.copyOf(".codex", "delta", "mine.md")), "and the file is still the user's")
	})

	// A name in shared/ and in a runtime's own folder would be two sources for one copy.
	for _, runtime := range []string{"claude", "codex"} {
		t.Run("a name in shared and in "+runtime+" is refused, check and install, before anything is copied", func(t *testing.T) {
			fx := newSkillsFixture(t).withSharedSkill(t)
			write(t, filepath.Join(fx.skills, runtime, "delta", "SKILL.md"), "the same name\n")
			before := fx.runtimeTree(t)

			for _, mode := range []string{"install", "check"} {
				out, code := fx.script(t, repoRoot, mode)
				assert.NotEqual(t, 0, code, mode+": "+out)
				assert.Contains(t, out, filepath.Join(fx.skills, "shared", "delta"), "it names the shared folder")
				assert.Contains(t, out, filepath.Join(fx.skills, runtime, "delta"), "and the other")
			}
			assert.Equal(t, before, fx.runtimeTree(t), "not even the skills that do not clash")
			assert.NoDirExists(t, fx.copyOf(".claude", "alpha"))
		})
	}

	t.Run("a dangling link with the name of a shared skill is a clash too", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "nowhere"), filepath.Join(fx.skills, "codex", "delta")))
		before := fx.runtimeTree(t)

		out, code := fx.script(t, repoRoot, "install")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, filepath.Join(fx.skills, "codex", "delta"))
		assert.Equal(t, before, fx.runtimeTree(t))
	})

	t.Run("the same name in shared and in antigravity is not a clash: that copy goes elsewhere", func(t *testing.T) {
		fx := newSkillsFixture(t).withSharedSkill(t)
		write(t, filepath.Join(fx.skills, "antigravity", "delta", "SKILL.md"), "antigravity's own\n")

		out := install(t, fx)

		assert.Contains(t, out, "installed  claude/delta")
		assert.Contains(t, out, "skipped    antigravity/delta")
	})
}
