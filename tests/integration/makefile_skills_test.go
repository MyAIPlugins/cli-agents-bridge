//go:build !windows

package integration

import (
	"archive/zip"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `make install-skills` and `make skills-check`: the skills the agents load are the
// repo's, in skills/<runtime>/<name>/, and the installed copies are copies of those.
//
// Every run is against a fixture of skills and a HOME inside t.TempDir(): never the
// real home, and never the repo's own skills. Both paths have a space in them on
// purpose (the Makefile has been bitten by that before), and the environment is set so
// that nothing here can leave files in the module cache that a temp dir could not
// remove (F-149): the targets run no build at all, and this checks that nothing but
// the folders it names appeared in HOME.

type skillsFixture struct {
	skills string // the fixture standing in for the repo's skills/
	home   string
	bin    string
	goDir  string // where the go command run by the Makefile keeps its cache, outside HOME
}

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, path)
	return string(data)
}

func newSkillsFixture(t *testing.T) skillsFixture {
	t.Helper()
	tmp := t.TempDir()
	fx := skillsFixture{skills: filepath.Join(tmp, "my skills"), home: filepath.Join(tmp, "ho me"), bin: filepath.Join(tmp, "out bin"), goDir: filepath.Join(tmp, "go")}
	write(t, filepath.Join(fx.skills, "claude", "alpha", "SKILL.md"), "alpha for claude\n")
	write(t, filepath.Join(fx.skills, "claude", "alpha", "refs", "guide.md"), "guide\n")
	write(t, filepath.Join(fx.skills, "codex", "alpha", "SKILL.md"), "alpha for codex\n")
	write(t, filepath.Join(fx.skills, "antigravity", "beta", "SKILL.md"), "beta\n")
	// The user's own skill, next to the repo's: not the repo's to read or touch.
	write(t, filepath.Join(fx.home, ".claude", "skills", "personal", "SKILL.md"), "the user's own\n")
	require.NoError(t, os.MkdirAll(filepath.Join(fx.home, ".codex"), 0o700))
	// No ~/.gemini: antigravity is not on this machine.
	return fx
}

// withDesktopSkill adds a skill for Claude Desktop, which makes `install` need zip.
func (fx skillsFixture) withDesktopSkill(t *testing.T) skillsFixture {
	t.Helper()
	write(t, filepath.Join(fx.skills, "claude-desktop", "gamma", "SKILL.md"), "gamma\n")
	return fx
}

func (fx skillsFixture) env(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "HOME", "SKILLS_DIR", "BIN_DIR", "FORCE", "GOFLAGS":
		default:
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+fx.home, "GOFLAGS=-modcacherw", "GOCACHE="+fx.goDir+"/cache", "GOPATH="+fx.goDir+"/path", "FORCE=", "NO_COLOR=1")
}

// make runs a target of the repo's Makefile and returns what it printed and its exit code.
func (fx skillsFixture) make(t *testing.T, repoRoot string, target string, extra ...string) (string, int) {
	t.Helper()
	// On the command line, not in the environment: the Makefile assigns BIN_DIR itself,
	// and an assignment in the Makefile beats the environment. The first version of this
	// test passed BIN_DIR in the environment and the zip went into the repo's bin/.
	args := append([]string{"--no-print-directory", "-C", repoRoot, target, "SKILLS_DIR=" + fx.skills, "BIN_DIR=" + fx.bin}, extra...)
	cmd := exec.Command("make", args...)
	cmd.Env = fx.env()
	out, err := cmd.CombinedOutput()
	return string(out), exitCode(t, err)
}

// script runs scripts/skills.sh itself, to see the exit code make hides (make turns any
// failed recipe into 2).
func (fx skillsFixture) script(t *testing.T, repoRoot, mode string, extra ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(repoRoot, "scripts", "skills.sh"), mode)
	cmd.Env = append(fx.env(), "SKILLS_DIR="+fx.skills, "BIN_DIR="+fx.bin)
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), exitCode(t, err)
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "the command did not run: %v", err)
	return exit.ExitCode()
}

// runtimeTree is every file under the three runtimes' folders with its content: what
// "touches nothing" is measured on. Not the whole HOME: the Makefile runs `go env` while
// it is parsed, and the go command writes its telemetry counters under HOME (Library/ on
// a Mac, .config/ elsewhere) on any run, so a count there changes whatever the target.
func (fx skillsFixture) runtimeTree(t *testing.T) map[string]string {
	t.Helper()
	all := map[string]string{}
	for _, dir := range []string{".claude", ".codex", ".gemini"} {
		if _, err := os.Stat(filepath.Join(fx.home, dir)); err != nil {
			continue
		}
		for k, v := range tree(t, filepath.Join(fx.home, dir)) {
			all[k] = v
		}
	}
	return all
}

// tree is every file under dir with its content.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[path] = string(data)
		return nil
	}))
	return out
}

func TestMakefile_Skills(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	_, zipErr := exec.LookPath("zip")

	installed := func(fx skillsFixture, runtimeDir, name, file string) string {
		return filepath.Join(fx.home, runtimeDir, "skills", name, file)
	}

	t.Run("installs the skills of the repo, only those, and skips a runtime that is not here", func(t *testing.T) {
		fx := newSkillsFixture(t)
		personal := tree(t, filepath.Join(fx.home, ".claude", "skills", "personal"))

		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)

		assert.Equal(t, "alpha for claude\n", readFile(t, installed(fx, ".claude", "alpha", "SKILL.md")))
		assert.Equal(t, "guide\n", readFile(t, installed(fx, ".claude", "alpha", "refs/guide.md")))
		assert.Equal(t, "alpha for codex\n", readFile(t, installed(fx, ".codex", "alpha", "SKILL.md")))
		assert.Equal(t, personal, tree(t, filepath.Join(fx.home, ".claude", "skills", "personal")), "the user's own skill is untouched")
		assert.NotContains(t, out, "personal", "and not even named")

		assert.Contains(t, out, "skipped    antigravity/beta", "a runtime that is not on this machine is said, not an error")
		assert.NoDirExists(t, filepath.Join(fx.home, ".gemini"), "and not created")

		entries, err := os.ReadDir(fx.home)
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		var others []string
		for _, n := range names {
			if n != "Library" && n != ".config" { // the go command's telemetry, see runtimeTree
				others = append(others, n)
			}
		}
		assert.Equal(t, []string{".claude", ".codex"}, others, "nothing else appeared in HOME (a module cache, say)")

		again, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, again)
		assert.Contains(t, again, "unchanged  claude/alpha")
		assert.NotContains(t, again, "installed ")
	})

	t.Run("Claude Desktop gets a zip with the folder at its root, and where to upload it", func(t *testing.T) {
		if zipErr != nil {
			t.Skip("zip not available")
		}
		fx := newSkillsFixture(t).withDesktopSkill(t)
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)

		assert.Contains(t, out, "Customize > Skills > Upload a skill")
		r, err := zip.OpenReader(filepath.Join(fx.bin, "gamma.zip"))
		require.NoError(t, err)
		defer r.Close()
		var names []string
		for _, f := range r.File {
			names = append(names, f.Name)
		}
		assert.Contains(t, names, "gamma/SKILL.md", "the folder is the root of the zip")
		assert.NoDirExists(t, filepath.Join(fx.home, ".claude", "skills", "gamma"), "and nothing is copied for Desktop")
	})

	t.Run("without zip a Desktop skill refuses the whole install, before anything is copied", func(t *testing.T) {
		fx := newSkillsFixture(t).withDesktopSkill(t)
		// A PATH with the tools the script uses and no zip.
		bin := t.TempDir()
		for _, tool := range []string{"basename", "cmp", "cp", "dirname", "find", "grep", "mkdir", "rm", "sed", "sort"} {
			path, err := exec.LookPath(tool)
			require.NoError(t, err, tool)
			require.NoError(t, os.Symlink(path, filepath.Join(bin, tool)))
		}
		out, code := fx.script(t, repoRoot, "install", "PATH="+bin)
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "zip is not installed")
		assert.NoDirExists(t, filepath.Join(fx.home, ".claude", "skills", "alpha"), "and nothing was copied")
	})

	t.Run("a copy that differs is refused, named, and nothing is copied", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		write(t, installed(fx, ".claude", "alpha", "SKILL.md"), "alpha for claude\nA LOCAL EDIT\n")
		require.NoError(t, os.RemoveAll(filepath.Join(fx.home, ".codex", "skills", "alpha")))

		out, code = fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "refused")
		assert.Contains(t, out, "claude/alpha")
		assert.Contains(t, out, "differs: SKILL.md")
		assert.Equal(t, "alpha for claude\nA LOCAL EDIT\n", readFile(t, installed(fx, ".claude", "alpha", "SKILL.md")), "the local edit is still there")
		assert.NoDirExists(t, filepath.Join(fx.home, ".codex", "skills", "alpha"), "all or nothing: the codex copy that was missing was not installed either")

		out, code = fx.make(t, repoRoot, "install-skills", "FORCE=1")
		require.Equal(t, 0, code, out)
		assert.Contains(t, out, "overwrote  claude/alpha: SKILL.md", "FORCE says what it overwrote")
		assert.Equal(t, "alpha for claude\n", readFile(t, installed(fx, ".claude", "alpha", "SKILL.md")))
		assert.Equal(t, "alpha for codex\n", readFile(t, installed(fx, ".codex", "alpha", "SKILL.md")), "and the missing one is installed")
	})

	// What each install leaves in a skill's folder is a record of what it installed, and
	// it is what tells a copy that is merely behind the repo from one somebody edited:
	// without it every update would need FORCE=1 and the protection would be a habit.
	const record = ".cab-installed"
	repoSkill := func(fx skillsFixture, file string) string {
		return filepath.Join(fx.skills, "claude", "alpha", file)
	}
	install := func(t *testing.T, fx skillsFixture, extra ...string) string {
		t.Helper()
		out, code := fx.make(t, repoRoot, "install-skills", extra...)
		require.Equal(t, 0, code, out)
		return out
	}

	t.Run("a copy that is only behind the repo is updated without FORCE, and check says it is behind", func(t *testing.T) {
		fx := newSkillsFixture(t)
		install(t, fx)
		write(t, repoSkill(fx, "SKILL.md"), "alpha for claude, newer\n")
		write(t, repoSkill(fx, "refs/new.md"), "a new file\n")

		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "different  claude/alpha  (behind the repo")
		assert.NotContains(t, out, "locally modified")

		out = install(t, fx)
		assert.Contains(t, out, "updated    claude/alpha: SKILL.md")
		assert.Contains(t, out, "updated    claude/alpha: refs/new.md")
		assert.NotContains(t, out, "overwrote")
		assert.Equal(t, "alpha for claude, newer\n", readFile(t, installed(fx, ".claude", "alpha", "SKILL.md")))
		assert.Equal(t, "a new file\n", readFile(t, installed(fx, ".claude", "alpha", "refs/new.md")))

		out, code = fx.make(t, repoRoot, "skills-check")
		assert.Equal(t, 0, code, out)

		// The update renewed the record: the next one is an update again, not an edit.
		write(t, repoSkill(fx, "SKILL.md"), "alpha for claude, newer still\n")
		assert.Contains(t, install(t, fx), "updated    claude/alpha: SKILL.md")
	})

	t.Run("a copy edited since the last install is refused even when the repo moved too", func(t *testing.T) {
		fx := newSkillsFixture(t)
		install(t, fx)
		write(t, installed(fx, ".claude", "alpha", "SKILL.md"), "alpha for claude\nMY EDIT\n")
		write(t, repoSkill(fx, "SKILL.md"), "alpha for claude, newer\n")

		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "different  claude/alpha  (locally modified")
		assert.Contains(t, out, "changed since the last install: SKILL.md")

		out, code = fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "refused")
		assert.Contains(t, out, "changed since the last install: SKILL.md")
		assert.Equal(t, "alpha for claude\nMY EDIT\n", readFile(t, installed(fx, ".claude", "alpha", "SKILL.md")), "the edit is still there")

		out = install(t, fx, "FORCE=1")
		assert.Contains(t, out, "overwrote  claude/alpha: SKILL.md")
		assert.Equal(t, "alpha for claude, newer\n", readFile(t, installed(fx, ".claude", "alpha", "SKILL.md")))
	})

	t.Run("a copy with no record is taken as edited, and the first FORCE is the last", func(t *testing.T) {
		fx := newSkillsFixture(t)
		// What is on the machine today: a copy somebody put there, different from the repo's.
		write(t, installed(fx, ".claude", "alpha", "SKILL.md"), "an old alpha\n")

		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "different  claude/alpha  (locally modified")
		assert.Contains(t, out, "no record of the last install")

		out, code = fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "no record of the last install")
		assert.Equal(t, "an old alpha\n", readFile(t, installed(fx, ".claude", "alpha", "SKILL.md")))

		install(t, fx, "FORCE=1")
		assert.FileExists(t, installed(fx, ".claude", "alpha", record), "the install records what it installed")

		write(t, repoSkill(fx, "SKILL.md"), "alpha for claude, newer\n")
		out = install(t, fx)
		assert.Contains(t, out, "updated    claude/alpha: SKILL.md", "no FORCE the second time")
	})

	t.Run("a copy identical to the repo is adopted, and a check never writes the record", func(t *testing.T) {
		fx := newSkillsFixture(t)
		write(t, installed(fx, ".claude", "alpha", "SKILL.md"), "alpha for claude\n")
		write(t, installed(fx, ".claude", "alpha", "refs/guide.md"), "guide\n")

		out, _ := fx.make(t, repoRoot, "skills-check") // exits 1: the codex copy is not installed, which is not what this is about
		assert.Contains(t, out, "ok         claude/alpha")
		assert.NoFileExists(t, installed(fx, ".claude", "alpha", record), "a check touches nothing")

		out = install(t, fx)
		assert.Contains(t, out, "adopted    claude/alpha")
		assert.FileExists(t, installed(fx, ".claude", "alpha", record))
		assert.Contains(t, install(t, fx), "unchanged  claude/alpha")

		// A record that no longer says what the copy is, on a copy that is the repo's.
		write(t, installed(fx, ".claude", "alpha", record), "1 2 SKILL.md\n")
		assert.Contains(t, install(t, fx), "adopted    claude/alpha", "it is rewritten")

		write(t, repoSkill(fx, "SKILL.md"), "alpha for claude, newer\n")
		assert.Contains(t, install(t, fx), "updated    claude/alpha: SKILL.md", "and the adopted copy updates without FORCE")
	})

	t.Run("a file the record lists and the copy lost counts as an edit", func(t *testing.T) {
		fx := newSkillsFixture(t)
		install(t, fx)
		require.NoError(t, os.Remove(installed(fx, ".claude", "alpha", "refs/guide.md")))
		write(t, repoSkill(fx, "SKILL.md"), "alpha for claude, newer\n")

		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "locally modified")
		assert.Contains(t, out, "changed since the last install: refs/guide.md")
		out, code = fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
	})

	// "It touches only the folders of the skills the repo has" has to hold through a link
	// too: cp follows a symbolic link in the destination and writes into its target.
	// A target is planted outside everything the install may touch, and each case checks
	// it is exactly as it was, with and without FORCE=1.
	outside := func(t *testing.T) (dir string, before map[string]string) {
		t.Helper()
		dir = filepath.Join(t.TempDir(), "somewhere else")
		write(t, filepath.Join(dir, "keep.md"), "not the install's to touch\n")
		write(t, filepath.Join(dir, "SKILL.md"), "not the install's either\n")
		return dir, tree(t, dir)
	}
	refusedThroughLink := func(t *testing.T, fx skillsFixture, link string, elsewhere string, before map[string]string) {
		t.Helper()
		for _, extra := range [][]string{nil, {"FORCE=1"}} {
			out, code := fx.make(t, repoRoot, "install-skills", extra...)
			assert.NotEqual(t, 0, code, "%v: %s", extra, out)
			assert.Contains(t, out, "refused", "%v", extra)
			assert.Contains(t, out, link, "the path of the link is named: %v", extra)
			assert.Equal(t, before, tree(t, elsewhere), "nothing was written through it: %v", extra)
			assert.NoDirExists(t, filepath.Join(fx.home, ".codex", "skills", "alpha"), "and nothing else was copied: %v", extra)
		}
		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "linked     ")
		assert.Contains(t, out, link)
		assert.Equal(t, before, tree(t, elsewhere), "a check does not read through it either")
	}

	t.Run("a skill folder that is a symbolic link is refused, with or without FORCE", func(t *testing.T) {
		fx := newSkillsFixture(t)
		elsewhere, before := outside(t)
		link := filepath.Join(fx.home, ".claude", "skills", "alpha")
		require.NoError(t, os.Symlink(elsewhere, link))
		refusedThroughLink(t, fx, link, elsewhere, before)
	})

	t.Run("a check where only a link is wrong still exits non-zero", func(t *testing.T) {
		fx := newSkillsFixture(t)
		install(t, fx)
		out, code := fx.make(t, repoRoot, "skills-check")
		require.Equal(t, 0, code, "the premise: everything is ok: %s", out)
		elsewhere, _ := outside(t)
		copyOfAlpha := filepath.Join(fx.home, ".claude", "skills", "alpha")
		require.NoError(t, os.RemoveAll(copyOfAlpha))
		require.NoError(t, os.Symlink(elsewhere, copyOfAlpha))

		out, code = fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "linked     claude/alpha")
	})

	t.Run("a dangling link where the skill folder should be is refused, not created through", func(t *testing.T) {
		fx := newSkillsFixture(t)
		gone := filepath.Join(t.TempDir(), "not there")
		link := filepath.Join(fx.home, ".claude", "skills", "alpha")
		require.NoError(t, os.Symlink(gone, link))
		out, code := fx.make(t, repoRoot, "install-skills", "FORCE=1")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "symbolic link", "it is the link that is named, not an accident of mkdir")
		assert.Contains(t, out, link)
		assert.NoDirExists(t, gone, "the target of the link was not created")
	})

	t.Run("a symbolic link in a skill for Claude Desktop is refused before it is zipped", func(t *testing.T) {
		fx := newSkillsFixture(t).withDesktopSkill(t)
		elsewhere, before := outside(t)
		link := filepath.Join(fx.skills, "claude-desktop", "gamma", "outside.md")
		require.NoError(t, os.Symlink(filepath.Join(elsewhere, "keep.md"), link))
		out, code := fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, link)
		assert.NoFileExists(t, filepath.Join(fx.bin, "gamma.zip"))
		assert.Equal(t, before, tree(t, elsewhere))
	})

	t.Run("a file inside the installed copy that is a symbolic link is refused", func(t *testing.T) {
		fx := newSkillsFixture(t)
		elsewhere, before := outside(t)
		write(t, installed(fx, ".claude", "alpha", "refs/guide.md"), "guide\n")
		link := installed(fx, ".claude", "alpha", "SKILL.md")
		require.NoError(t, os.Symlink(filepath.Join(elsewhere, "SKILL.md"), link))
		refusedThroughLink(t, fx, link, elsewhere, before)
	})

	t.Run("a folder inside the installed copy that is a symbolic link is refused", func(t *testing.T) {
		fx := newSkillsFixture(t)
		elsewhere, before := outside(t)
		write(t, installed(fx, ".claude", "alpha", "SKILL.md"), "alpha for claude\n")
		link := installed(fx, ".claude", "alpha", "refs")
		require.NoError(t, os.Symlink(elsewhere, link))
		refusedThroughLink(t, fx, link, elsewhere, before)
	})

	t.Run("a symbolic link in the repo's own skill is refused too", func(t *testing.T) {
		fx := newSkillsFixture(t)
		elsewhere, before := outside(t)
		link := repoSkill(fx, "refs/linked.md")
		require.NoError(t, os.Symlink(filepath.Join(elsewhere, "keep.md"), link))
		refusedThroughLink(t, fx, link, elsewhere, before)
		assert.NoDirExists(t, filepath.Join(fx.home, ".claude", "skills", "alpha"))
	})

	t.Run("the skills folder of a runtime being a link is the user's arrangement and works", func(t *testing.T) {
		fx := newSkillsFixture(t)
		dotfiles := filepath.Join(t.TempDir(), "dotfiles", "skills")
		write(t, filepath.Join(dotfiles, "personal", "SKILL.md"), "the user's own\n")
		require.NoError(t, os.RemoveAll(filepath.Join(fx.home, ".claude", "skills")))
		require.NoError(t, os.Symlink(dotfiles, filepath.Join(fx.home, ".claude", "skills")))

		out := install(t, fx)
		assert.Contains(t, out, "installed  claude/alpha")
		assert.Equal(t, "alpha for claude\n", readFile(t, filepath.Join(dotfiles, "alpha", "SKILL.md")), "under the link, in the skill's own folder")
		assert.Equal(t, "the user's own\n", readFile(t, filepath.Join(dotfiles, "personal", "SKILL.md")))
	})

	t.Run("the record is no part of any comparison and is never named as an extra", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out := install(t, fx)
		assert.NotContains(t, out, record)
		check, code := fx.make(t, repoRoot, "skills-check")
		assert.Equal(t, 0, code, check)
		assert.NotContains(t, check, record)
		assert.NotContains(t, check, "only in the installed copy")
	})

	t.Run("a file only in the installed copy is listed and never deleted", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		write(t, installed(fx, ".claude", "alpha", "refs/mine.md"), "my own notes\n")

		out, code = fx.make(t, repoRoot, "skills-check")
		require.Equal(t, 0, code, "an extra file is not a difference: %s", out)
		assert.Contains(t, out, "only in the installed copy, left alone: refs/mine.md")

		out, code = fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		assert.Equal(t, "my own notes\n", readFile(t, installed(fx, ".claude", "alpha", "refs/mine.md")))

		write(t, installed(fx, ".claude", "alpha", "SKILL.md"), "edited\n")
		out, code = fx.make(t, repoRoot, "install-skills", "FORCE=1")
		require.Equal(t, 0, code, out)
		assert.Equal(t, "my own notes\n", readFile(t, installed(fx, ".claude", "alpha", "refs/mine.md")), "FORCE does not delete either")
		assert.Contains(t, out, "only in the installed copy, left alone: refs/mine.md")
	})

	t.Run("a runtime without its folder is skipped and not created", func(t *testing.T) {
		fx := newSkillsFixture(t)
		require.NoError(t, os.RemoveAll(filepath.Join(fx.home, ".codex")))
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		assert.Contains(t, out, "skipped    codex/alpha")
		assert.NoDirExists(t, filepath.Join(fx.home, ".codex"))
	})

	t.Run("skills-check says ok, missing or different, exits non-zero unless all is ok, and touches nothing", func(t *testing.T) {
		fx := newSkillsFixture(t).withDesktopSkill(t)

		before := fx.runtimeTree(t)
		out, code := fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "missing    claude/alpha")
		assert.Equal(t, before, fx.runtimeTree(t), "a check changes nothing")
		assert.NoDirExists(t, fx.bin, "not even the zips")
		_, direct := fx.script(t, repoRoot, "check")
		assert.Equal(t, 1, direct, "the script itself exits 1 (make says 2 for any failed recipe)")

		out, code = fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		out, code = fx.make(t, repoRoot, "skills-check")
		assert.Equal(t, 0, code, out)
		assert.Contains(t, out, "ok         claude/alpha")
		assert.Contains(t, out, "manual     claude-desktop/gamma")

		write(t, installed(fx, ".claude", "alpha", "refs/guide.md"), "changed\n")
		before = fx.runtimeTree(t)
		out, code = fx.make(t, repoRoot, "skills-check")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "different  claude/alpha")
		assert.Contains(t, out, "differs: refs/guide.md")
		assert.Equal(t, before, fx.runtimeTree(t))
	})

	t.Run("a folder that is no runtime the script knows is refused, not skipped", func(t *testing.T) {
		fx := newSkillsFixture(t)
		write(t, filepath.Join(fx.skills, "windsurf", "delta", "SKILL.md"), "delta\n")
		out, code := fx.make(t, repoRoot, "install-skills")
		assert.NotEqual(t, 0, code, out)
		assert.Contains(t, out, "windsurf")
		assert.NoDirExists(t, filepath.Join(fx.home, ".claude", "skills", "alpha"), "and nothing was copied")
	})

	t.Run("the help line says what the target is and that it is not for Windows", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out, code := fx.make(t, repoRoot, "help")
		require.Equal(t, 0, code, out)
		for _, target := range []string{"install-skills", "skills-check"} {
			var line string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, target) {
					line = l
				}
			}
			assert.Contains(t, line, "not for Windows", target)
		}
	})
}
