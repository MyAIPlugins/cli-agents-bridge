//go:build !windows

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-157, the old folder. Codex reads ~/.agents/skills and, still, ~/.codex/skills, and
// a name found in both is listed twice in its prompt (measured with 0.166.0, and with
// the same name behind a link: the cases are in the BAR). So a skill of the repo that is
// also in the old folder, as a folder other than the one the install writes to, is a
// second copy, and the install and the check refuse it, whatever FORCE says. The same
// folder reached by two paths is one copy, and is not refused.
//
// Every test that says "refused" says it with the premise beside it: the remedy the
// message prints (remove the old path by hand) is done, and the same install passes. A
// refusal that comes from something else would not go away with it.

// oldCopy is a path in Codex's old folder, in the fixture's HOME.
func (fx skillsFixture) oldCopy(path ...string) string {
	return filepath.Join(append([]string{fx.home, ".codex", "skills"}, path...)...)
}

// snapshot is every entry under the runtimes' folders of HOME, and under the extra
// directories, by absolute path: a folder, a file with its content, or a link with its
// target. It does not follow a link, so a link that was replaced by what it pointed to
// differs from it.
func (fx skillsFixture) snapshot(t *testing.T, extra ...string) map[string]string {
	t.Helper()
	var roots []string
	for _, dir := range []string{".agents", ".claude", ".codex", ".gemini"} {
		roots = append(roots, filepath.Join(fx.home, dir))
	}
	roots = append(roots, extra...)
	out := map[string]string{}
	for _, root := range roots {
		if _, err := os.Lstat(root); err != nil {
			continue
		}
		require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch {
			case d.Type()&os.ModeSymlink != 0:
				target, rerr := os.Readlink(path)
				if rerr != nil {
					return rerr
				}
				out[path] = "link -> " + target
			case d.IsDir():
				out[path] = "dir"
			default:
				data, rerr := os.ReadFile(path)
				if rerr != nil {
					return rerr
				}
				out[path] = "file " + string(data)
			}
			return nil
		}))
	}
	return out
}

func TestMakefile_SkillsOldCodexCopyBlocks(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	const record = "1 2 SKILL.md\n" // what an install of before this change left in the folder

	cases := []struct {
		name      string
		skill     string // the repo's skill the old path has the name of
		line      string // what check says about it
		installed bool   // the new copy is already there when the old one is found
		shared    bool
		plant     func(t *testing.T, fx skillsFixture, skill string) (watch []string)
	}{
		{
			name: "a folder with the name of a skill, put there by hand", skill: "alpha", line: "duplicate  codex/alpha",
			plant: func(t *testing.T, fx skillsFixture, skill string) []string {
				write(t, fx.oldCopy(skill, "SKILL.md"), "an old alpha\n")
				return nil
			},
		},
		{
			name: "a folder an install of before this change left, with its record", skill: "alpha", line: "duplicate  codex/alpha",
			plant: func(t *testing.T, fx skillsFixture, skill string) []string {
				write(t, fx.oldCopy(skill, "SKILL.md"), "alpha for codex\n")
				write(t, fx.oldCopy(skill, ".cab-installed"), record)
				return nil
			},
		},
		{
			name: "the same, when the new copy is already installed", skill: "alpha", line: "duplicate  codex/alpha", installed: true,
			plant: func(t *testing.T, fx skillsFixture, skill string) []string {
				write(t, fx.oldCopy(skill, "SKILL.md"), "alpha for codex\n")
				write(t, fx.oldCopy(skill, ".cab-installed"), record)
				return nil
			},
		},
		{
			name: "the old folder of a shared skill", skill: "delta", line: "duplicate  codex/delta", shared: true,
			plant: func(t *testing.T, fx skillsFixture, skill string) []string {
				write(t, fx.oldCopy(skill, "SKILL.md"), "delta, for both\n")
				write(t, fx.oldCopy(skill, ".cab-installed"), record)
				return nil
			},
		},
		{
			name: "a link to another copy, say the user's dotfiles", skill: "alpha", line: "duplicate  codex/alpha",
			plant: func(t *testing.T, fx skillsFixture, skill string) []string {
				dotfiles := filepath.Join(t.TempDir(), "dotfiles")
				write(t, filepath.Join(dotfiles, skill, "SKILL.md"), "the copy in the dotfiles\n")
				require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
				require.NoError(t, os.Symlink(filepath.Join(dotfiles, skill), fx.oldCopy(skill)))
				return []string{dotfiles}
			},
		},
		{
			name: "a link to no folder at all", skill: "alpha", line: "deadlink   codex/alpha",
			plant: func(t *testing.T, fx skillsFixture, skill string) []string {
				require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "not there"), fx.oldCopy(skill)))
				return nil
			},
		},
		{
			name: "a link to a plain file leads to no folder either", skill: "alpha", line: "deadlink   codex/alpha",
			plant: func(t *testing.T, fx skillsFixture, skill string) []string {
				target := filepath.Join(t.TempDir(), "a file")
				write(t, target, "not a folder\n")
				require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
				require.NoError(t, os.Symlink(target, fx.oldCopy(skill)))
				return []string{filepath.Dir(target)}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newSkillsFixture(t)
			if c.shared {
				fx = fx.withSharedSkill(t)
			}
			if c.installed {
				out, code := fx.make(t, repoRoot, "install-skills")
				require.Equal(t, 0, code, "premise: the install passes when nothing is in the old folder: %s", out)
			}
			var installedCopy map[string]string // what the install wrote, if it has
			if c.installed {
				installedCopy = tree(t, fx.agentsSkills(c.skill))
			}
			// Something an install would copy, so that "nothing was copied" is seen.
			write(t, filepath.Join(fx.skills, "claude", "alpha", "SKILL.md"), "alpha for claude, newer\n")
			watch := c.plant(t, fx, c.skill)
			old := fx.oldCopy(c.skill)
			before := fx.snapshot(t, watch...)
			outsideBefore := map[string]map[string]string{}
			for _, dir := range watch {
				outsideBefore[dir] = tree(t, dir)
			}

			var refusal string
			for _, extra := range [][]string{nil, {"FORCE=1"}} {
				out, code := fx.script(t, repoRoot, "install", extra...)
				refusal = out
				assert.Equal(t, 1, code, "%v: %s", extra, out)
				assert.Contains(t, out, "refused: a skill is also in the old folder", "%v", extra)
				assert.Contains(t, out, "codex/"+c.skill, "%v", extra)
				assert.Contains(t, out, old, "the path to remove is named: %v", extra)
				assert.NotContains(t, out, "run again with FORCE=1", "FORCE is not offered as the way out: %v", extra)
				assert.Equal(t, before, fx.snapshot(t, watch...), "nothing was copied, in any runtime, and the old copy is as it was: %v", extra)
			}

			out, code := fx.script(t, repoRoot, "check")
			assert.Equal(t, 1, code, out)
			assert.Contains(t, out, c.line)
			assert.Contains(t, out, old, "and the path")
			assert.NotContains(t, out, "duplicate  claude/", "a skill of Claude Code is not a duplicate of Codex's old folder")
			assert.NotContains(t, out, "deadlink   claude/")
			assert.Equal(t, before, fx.snapshot(t, watch...), "a check touches nothing")

			// The premise: the old path is the whole reason. Do what the message says,
			// to the letter, and the same install passes and copies everything. To the
			// letter means: the path as it is printed (a line of its own, no / at the end);
			// a link is removed as itself, and a folder with what is in it. It must not cost
			// the copy the install wrote, nor anything outside the old folder.
			assert.Contains(t, refusal, "\n    "+old+"\n", "the path is printed on a line of its own, with no / at the end")
			if info, lerr := os.Lstat(old); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
				assert.Contains(t, refusal, "a symbolic link to ", "a link is said to be one")
				assert.Contains(t, refusal, "remove the link", "and to be removed as a link")
				require.NoError(t, os.Remove(old))
			} else {
				require.NoError(t, os.RemoveAll(old))
			}
			for dir, was := range outsideBefore {
				assert.Equal(t, was, tree(t, dir), "nothing outside the old folder was lost by following the message: %s", dir)
			}
			if c.installed {
				assert.Equal(t, installedCopy, tree(t, fx.agentsSkills(c.skill)), "and the copy the install wrote is whole")
			}
			out, code = fx.script(t, repoRoot, "install")
			require.Equal(t, 0, code, "without the old copy the same install passes: %s", out)
			assert.Equal(t, "alpha for claude, newer\n", readFile(t, fx.copyOf(".claude", "alpha", "SKILL.md")))
			assert.Equal(t, "alpha for codex\n", readFile(t, fx.agentsSkills("alpha", "SKILL.md")))
			out, code = fx.script(t, repoRoot, "check")
			assert.Equal(t, 0, code, out)
			assert.NotContains(t, out, "duplicate")
			assert.NotContains(t, out, "deadlink")
		})
	}

	// With every other copy ok, the exit code of a check is the old copy's alone.
	for _, alone := range []struct {
		name, line string
		plant      func(t *testing.T, fx skillsFixture)
	}{
		{"a second folder", "duplicate  codex/alpha", func(t *testing.T, fx skillsFixture) {
			write(t, fx.oldCopy("alpha", "SKILL.md"), "an old alpha\n")
		}},
		{"a link to no folder", "deadlink   codex/alpha", func(t *testing.T, fx skillsFixture) {
			require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "not there"), fx.oldCopy("alpha")))
		}},
	} {
		t.Run("with every other copy ok, "+alone.name+" in the old folder alone makes the check exit 1", func(t *testing.T) {
			fx := newSkillsFixture(t)
			out, code := fx.make(t, repoRoot, "install-skills")
			require.Equal(t, 0, code, out)
			out, code = fx.script(t, repoRoot, "check")
			require.Equal(t, 0, code, "premise: everything is ok before the old copy: %s", out)

			alone.plant(t, fx)
			out, code = fx.script(t, repoRoot, "check")
			assert.Equal(t, 1, code, out)
			assert.Contains(t, out, alone.line)
			assert.Contains(t, out, "ok         codex/alpha", "and the copy itself is still said ok")
			assert.Contains(t, out, "ok         claude/alpha")
		})
	}

	// A folder that cannot be entered is still a folder with an inode of its own, and test -ef
	// needs no access inside it: its inode is not the new copy's, so it is another folder
	// and is refused.
	t.Run("an old folder that cannot be entered is refused, not taken for the same", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root enters any folder")
		}
		fx := newSkillsFixture(t)
		write(t, fx.oldCopy("alpha", "SKILL.md"), "an old alpha\n")
		require.NoError(t, os.Chmod(fx.oldCopy("alpha"), 0))
		t.Cleanup(func() { _ = os.Chmod(fx.oldCopy("alpha"), 0o700) })
		_, statErr := os.ReadFile(fx.oldCopy("alpha", "SKILL.md"))
		require.Error(t, statErr, "premise: the folder really cannot be entered")

		out, code := fx.script(t, repoRoot, "install", "FORCE=1")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, fx.oldCopy("alpha"))
		assert.NoDirExists(t, fx.agentsSkills("alpha"), "and nothing was copied")

		require.NoError(t, os.Chmod(fx.oldCopy("alpha"), 0o700))
		require.NoError(t, os.RemoveAll(fx.oldCopy("alpha")))
		out, code = fx.script(t, repoRoot, "install")
		assert.Equal(t, 0, code, "without it the same install passes: %s", out)
	})

	// The old limit (F-142) was here: "the same folder" was decided on the strings `pwd -P`
	// printed, and one folder spelled two ways on a volume that does not tell the cases
	// apart printed two, and was refused. It is now decided by device and inode (test -ef),
	// so the spelling does not matter. On a volume that does tell the cases apart, `.Agents`
	// is not `.agents`: the link leads to no folder, and is refused as that. Which one ran
	// is logged. (The first round of this test expected `duplicate` on the first kind of
	// volume: the expectation is inverted on purpose, see the report of the second round.)
	t.Run("a link that spells the new folder with other cases is the same folder where the volume does not tell cases apart", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)

		insensitive := volumeIgnoresCase(t)
		t.Logf("this volume tells upper from lower case apart: %v", !insensitive)

		require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
		spelled := filepath.Join(fx.home, ".Agents", "skills", "alpha")
		require.NoError(t, os.Symlink(spelled, fx.oldCopy("alpha")))
		before := fx.snapshot(t)

		if !insensitive {
			out, code = fx.script(t, repoRoot, "check")
			assert.Equal(t, 1, code, out)
			assert.Contains(t, out, "deadlink   codex/alpha")
			out, code = fx.script(t, repoRoot, "install", "FORCE=1")
			assert.Equal(t, 1, code, out)
			assert.Contains(t, out, fx.oldCopy("alpha"), "the refusal names the path to remove")
			assert.Equal(t, before, fx.snapshot(t), "and nothing was touched")
			return
		}
		oldInfo, err := os.Stat(fx.oldCopy("alpha"))
		require.NoError(t, err, "premise: the link leads to a folder")
		newInfo, err := os.Stat(fx.agentsSkills("alpha"))
		require.NoError(t, err)
		require.True(t, os.SameFile(oldInfo, newInfo), "premise: and it is the new copy's folder, spelled another way")

		out, code = fx.script(t, repoRoot, "check")
		assert.Equal(t, 0, code, out)
		assert.Contains(t, out, "ok         codex/alpha")
		assert.NotContains(t, out, "duplicate")
		out, code = fx.script(t, repoRoot, "install")
		assert.Equal(t, 0, code, out)
		assert.Contains(t, out, "unchanged  codex/alpha")
		assert.Equal(t, before, fx.snapshot(t), "and nothing was touched")
	})

	t.Run("an edited copy and an old copy are both named, and FORCE=1 alone does not help", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		write(t, fx.copyOf(".claude", "alpha", "SKILL.md"), "alpha for claude\nA LOCAL EDIT\n")
		write(t, fx.oldCopy("alpha", "SKILL.md"), "an old alpha\n")
		before := fx.snapshot(t)

		out, code = fx.script(t, repoRoot, "install")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "differs: SKILL.md", "the edit is named")
		assert.Contains(t, out, fx.oldCopy("alpha"), "and so is the old copy")
		assert.NotContains(t, out, "run again with FORCE=1", "the way out offered would not be one")
		assert.Equal(t, before, fx.snapshot(t))

		out, code = fx.script(t, repoRoot, "install", "FORCE=1")
		assert.Equal(t, 1, code, "FORCE=1 settles the edit, not the old copy: %s", out)
		assert.Equal(t, before, fx.snapshot(t))

		require.NoError(t, os.RemoveAll(fx.oldCopy("alpha")))
		out, code = fx.script(t, repoRoot, "install", "FORCE=1")
		require.Equal(t, 0, code, out)
		assert.Contains(t, out, "overwrote  claude/alpha: SKILL.md")
	})

	t.Run("only the skills of the repo are looked at: other folders of the old folder are neither read nor listed", func(t *testing.T) {
		fx := newSkillsFixture(t)
		system := fx.oldCopy(".system")
		write(t, filepath.Join(system, "SKILL.md"), "Codex's own\n")
		write(t, fx.oldCopy("personal", "SKILL.md"), "the user's own\n")
		write(t, fx.oldCopy("alpha-notes", "SKILL.md"), "a name that only starts like a skill of the repo\n")
		// A folder that cannot be entered makes a read an error with its name in it.
		require.NoError(t, os.Chmod(system, 0))
		t.Cleanup(func() { _ = os.Chmod(system, 0o700) })

		for _, mode := range []string{"check", "install"} {
			out, code := fx.script(t, repoRoot, mode)
			if mode == "install" {
				assert.Equal(t, 0, code, out)
			}
			assert.NotContains(t, out, ".system", mode)
			assert.NotContains(t, out, "personal", mode)
			assert.NotContains(t, out, "alpha-notes", mode)
			assert.NotContains(t, out, "denied", mode)
			assert.NotContains(t, out, "duplicate", mode)
		}
		assert.Equal(t, "alpha for codex\n", readFile(t, fx.agentsSkills("alpha", "SKILL.md")), "and the install passed")
	})
}

func TestMakefile_SkillsOldCodexFolderThatIsTheSameFolderPasses(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	// sameFolder is the premise of the three cases that pass because the old path and the
	// new one are one folder: the old path is there (a test where it is not would pass for
	// the wrong reason), and it is the very folder the install wrote.
	sameFolder := func(t *testing.T, fx skillsFixture) {
		t.Helper()
		_, err := os.Lstat(fx.oldCopy("alpha"))
		require.NoError(t, err, "premise: the old path exists")
		old, err := filepath.EvalSymlinks(fx.oldCopy("alpha"))
		require.NoError(t, err)
		now, err := filepath.EvalSymlinks(fx.agentsSkills("alpha"))
		require.NoError(t, err)
		require.Equal(t, now, old, "premise: and it is the same folder as the new copy")
	}

	cases := []struct {
		name    string
		arrange func(t *testing.T, fx skillsFixture) // before the first install
		after   func(t *testing.T, fx skillsFixture) // once the copy is installed
	}{
		{
			name: "~/.codex/skills is a link to ~/.agents/skills",
			arrange: func(t *testing.T, fx skillsFixture) {
				require.NoError(t, os.MkdirAll(fx.agentsSkills(), 0o700))
				require.NoError(t, os.Symlink(fx.agentsSkills(), fx.oldCopy()))
			},
			after: sameFolder,
		},
		{
			name: "~/.agents/skills is a link to ~/.codex/skills, the user's arrangement the other way",
			arrange: func(t *testing.T, fx skillsFixture) {
				require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
				require.NoError(t, os.MkdirAll(filepath.Join(fx.home, ".agents"), 0o700))
				require.NoError(t, os.Symlink(fx.oldCopy(), fx.agentsSkills()))
			},
			after: func(t *testing.T, fx skillsFixture) {
				sameFolder(t, fx)
				assert.Equal(t, "alpha for codex\n", readFile(t, fx.oldCopy("alpha", "SKILL.md")), "the copy is where the user's link says")
			},
		},
		{
			name: "~/.codex/skills/alpha is a link to the installed copy",
			arrange: func(t *testing.T, fx skillsFixture) {
				out, code := fx.make(t, repoRoot, "install-skills")
				require.Equal(t, 0, code, out)
				require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
				require.NoError(t, os.Symlink(fx.agentsSkills("alpha"), fx.oldCopy("alpha")))
			},
			after: sameFolder,
		},
		{
			name: "a plain file with the name of the skill in the old folder",
			arrange: func(t *testing.T, fx skillsFixture) {
				write(t, fx.oldCopy("alpha"), "not a folder, and not a skill\n")
			},
			after: func(t *testing.T, fx skillsFixture) {
				info, err := os.Lstat(fx.oldCopy("alpha"))
				require.NoError(t, err, "premise: the old path exists")
				require.True(t, info.Mode().IsRegular(), "premise: and is a file")
				assert.Equal(t, "not a folder, and not a skill\n", readFile(t, fx.oldCopy("alpha")), "left as it was")
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newSkillsFixture(t)
			c.arrange(t, fx)

			out, code := fx.script(t, repoRoot, "install")
			require.Equal(t, 0, code, out)
			c.after(t, fx)
			assert.NotContains(t, out, "duplicate")

			// The second run is where a rule that looked only at "the old path exists"
			// would refuse the copy the first one wrote.
			again, code := fx.script(t, repoRoot, "install")
			assert.Equal(t, 0, code, again)
			assert.Contains(t, again, "unchanged  codex/alpha")
			checked, code := fx.script(t, repoRoot, "check")
			assert.Equal(t, 0, code, checked)
			assert.Contains(t, checked, "ok         codex/alpha")
			assert.NotContains(t, checked, "duplicate")
			assert.NotContains(t, checked, "deadlink")
		})
	}

	// The case beside them: the new copy's folder is a link to the old copy. It is the
	// same folder, so it is no duplicate; it is refused for what it is, a link at the
	// place the install would write.
	t.Run("the new copy being a link to the old one is refused as a link, not as a duplicate", func(t *testing.T) {
		fx := newSkillsFixture(t)
		write(t, fx.oldCopy("alpha", "SKILL.md"), "alpha for codex\n")
		require.NoError(t, os.MkdirAll(fx.agentsSkills(), 0o700))
		require.NoError(t, os.Symlink(fx.oldCopy("alpha"), fx.agentsSkills("alpha")))
		before := fx.snapshot(t)

		out, code := fx.script(t, repoRoot, "check")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "linked     codex/alpha")
		assert.NotContains(t, out, "duplicate")

		out, code = fx.script(t, repoRoot, "install", "FORCE=1")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "a symbolic link is in the way")
		assert.NotContains(t, out, "old folder")
		assert.Equal(t, before, fx.snapshot(t), "nothing was copied")
	})
}
