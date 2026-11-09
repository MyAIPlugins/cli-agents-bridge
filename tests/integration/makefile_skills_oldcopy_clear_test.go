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

// F-157, second round of the diff gate. The old-copy refusal tells the user what to do, and
// a message that is followed to the letter must never cost the copy the install writes, nor
// anything outside ~/.codex/skills. Two ways it did: the line for a link to another folder
// did not say it was a link, so removing it through a / at the end of its path removed the
// folder it leads to; and a folder that was the installed copy itself, reached by an
// arrangement the path strings did not show (a link above it, spelled with other cases), was
// called "another copy". The table of makefile_skills_oldcopy_test.go now follows the message
// to the letter in every case it refuses; these are the cases that need their own setup.

// volumeIgnoresCase says whether the volume the tests write to takes "CaseProbe" and
// "caseprobe" for one name.
func volumeIgnoresCase(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "CaseProbe"), "x\n")
	_, err := os.Stat(filepath.Join(dir, "caseprobe"))
	return err == nil
}

func TestMakefile_SkillsOldCopyMessagesAreSafeToFollow(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	t.Run("a link to another copy is said to be a link, with where it leads and what to remove", func(t *testing.T) {
		fx := newSkillsFixture(t)
		target := filepath.Join(t.TempDir(), "dotfiles", "alpha")
		write(t, filepath.Join(target, "SKILL.md"), "the copy in the dotfiles\n")
		require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
		require.NoError(t, os.Symlink(target, fx.oldCopy("alpha")))
		old := fx.oldCopy("alpha")

		out, code := fx.script(t, repoRoot, "check")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "duplicate  codex/alpha  ("+old+" is a symbolic link to "+target+", another copy")
		assert.Contains(t, out, "a symbolic link to "+target+": remove the link, not the folder it leads to")

		out, code = fx.script(t, repoRoot, "install")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "a symbolic link to "+target+": remove the link, not the folder it leads to")
		assert.Contains(t, out, "never with a / at the end of its path")
		assert.NotContains(t, out, old+"/", "the path is never printed with a / at the end")

		// The same name as a real folder is not called a link.
		require.NoError(t, os.Remove(old))
		write(t, filepath.Join(old, "SKILL.md"), "a real folder\n")
		out, code = fx.script(t, repoRoot, "install")
		assert.Equal(t, 1, code, out)
		assert.NotContains(t, out, "symbolic link to", "a folder is not a link")
	})

	// A link to a path that is not a folder yet may be the new copy's own path, before the
	// first install: then it is not a second copy when the folder appears.
	t.Run("a link to the new copy's path, before the install, is a dead link that says what would make it a second copy", func(t *testing.T) {
		fx := newSkillsFixture(t)
		require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
		require.NoError(t, os.Symlink(fx.agentsSkills("alpha"), fx.oldCopy("alpha")))

		out, code := fx.script(t, repoRoot, "check")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, "deadlink   codex/alpha")
		assert.Contains(t, out, "if its target came back as a folder other than the new copy")
		assert.Contains(t, out, "a symbolic link to "+fx.agentsSkills("alpha")+", which is not a folder: remove the link")

		// The claim in the line: when the target comes back as the new copy, it is none.
		write(t, fx.agentsSkills("alpha", "SKILL.md"), "alpha for codex\n")
		out, _ = fx.script(t, repoRoot, "check")
		assert.NotContains(t, out, "deadlink")
		assert.NotContains(t, out, "duplicate")
		assert.Contains(t, out, "ok         codex/alpha")
	})

	// The message says where to move a folder to keep it, and the move works: the record
	// goes with it, so the copy is its own again, and a newer repo updates it without FORCE.
	t.Run("a folder moved to the place the message names is the installed copy again", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		// What an install of before this change left: the same folder, record included.
		require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700))
		require.NoError(t, os.Rename(fx.agentsSkills("alpha"), fx.oldCopy("alpha")))
		require.FileExists(t, fx.oldCopy("alpha", ".cab-installed"), "premise: it has its record")
		move := "to keep it, move it to " + fx.agentsSkills("alpha")

		for _, mode := range []string{"check", "install"} {
			out, code = fx.script(t, repoRoot, mode)
			assert.Equal(t, 1, code, mode+": "+out)
			assert.Contains(t, out, move, mode)
		}

		require.NoError(t, os.Rename(fx.oldCopy("alpha"), fx.agentsSkills("alpha")))
		out, code = fx.script(t, repoRoot, "install")
		require.Equal(t, 0, code, "after the move the install passes: %s", out)
		assert.Contains(t, out, "unchanged  codex/alpha", "and the copy is recognised as its own")

		write(t, filepath.Join(fx.skills, "codex", "alpha", "SKILL.md"), "alpha for codex, newer\n")
		out, code = fx.script(t, repoRoot, "install")
		require.Equal(t, 0, code, out)
		assert.Contains(t, out, "updated    codex/alpha: SKILL.md", "behind the repo, so no FORCE")
		assert.NotContains(t, out, "overwrote")
	})

	t.Run("the move is not offered when the new copy is already there: it would go inside it", func(t *testing.T) {
		fx := newSkillsFixture(t)
		out, code := fx.make(t, repoRoot, "install-skills")
		require.Equal(t, 0, code, out)
		write(t, fx.oldCopy("alpha", "SKILL.md"), "an old alpha\n")

		out, code = fx.script(t, repoRoot, "install")
		assert.Equal(t, 1, code, out)
		assert.Contains(t, out, fx.oldCopy("alpha"))
		assert.NotContains(t, out, "to keep it, move it", "mv onto a folder that exists puts the folder inside it")
	})

	// The new copy's path is a link to nothing: it does not exist as a folder, but mv of a
	// folder onto it fails (measured: "Not a directory"), so the move is not offered either.
	t.Run("the move is not offered when the new copy's path is a dead link: mv onto it fails", func(t *testing.T) {
		fx := newSkillsFixture(t)
		write(t, fx.oldCopy("alpha", "SKILL.md"), "an old alpha\n")
		require.NoError(t, os.MkdirAll(fx.agentsSkills(), 0o700))
		require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "nowhere"), fx.agentsSkills("alpha")))

		for _, mode := range []string{"check", "install"} {
			out, code := fx.script(t, repoRoot, mode)
			assert.Equal(t, 1, code, mode+": "+out)
			assert.Contains(t, out, fx.oldCopy("alpha"), mode)
			assert.NotContains(t, out, "to keep it, move it", mode)
		}

		// The premise the claim rests on, asserted here and not only measured once.
		cmd := exec.Command("mv", fx.oldCopy("alpha"), fx.agentsSkills("alpha"))
		raw, mvErr := cmd.CombinedOutput()
		require.Error(t, mvErr, "premise: mv of a folder onto a dead link fails: %s", raw)
		assert.DirExists(t, fx.oldCopy("alpha"), "and it left the folder where it was")
	})

	// An arrangement with a link ABOVE the skill: the old path is a real folder, not a link,
	// and it is the very folder of the installed copy. Two spellings of one folder only exist
	// on a volume that does not tell the cases apart; on any other the test says so and stops.
	for _, arrangement := range []struct {
		name    string
		arrange func(t *testing.T, fx skillsFixture)
	}{
		{"~/.agents/skills is a link to ~/.Codex/skills", func(t *testing.T, fx skillsFixture) {
			require.NoError(t, os.MkdirAll(fx.oldCopy(), 0o700)) // the folder the link leads to
			require.NoError(t, os.MkdirAll(filepath.Join(fx.home, ".agents"), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(fx.home, ".Codex", "skills"), fx.agentsSkills()))
		}},
		{"~/.codex/skills is a link to ~/.Agents/skills", func(t *testing.T, fx skillsFixture) {
			require.NoError(t, os.MkdirAll(fx.agentsSkills(), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(fx.home, ".Agents", "skills"), fx.oldCopy()))
		}},
	} {
		t.Run("the folder of the installed copy reached through a link spelled with other cases: "+arrangement.name, func(t *testing.T) {
			if !volumeIgnoresCase(t) {
				t.Skip("this volume tells upper from lower case apart: the two spellings are two folders here")
			}
			fx := newSkillsFixture(t)
			arrangement.arrange(t, fx)

			out, code := fx.script(t, repoRoot, "install")
			require.Equal(t, 0, code, out)
			oldInfo, err := os.Stat(fx.oldCopy("alpha"))
			require.NoError(t, err, "premise: the old path is there")
			newInfo, err := os.Stat(fx.agentsSkills("alpha"))
			require.NoError(t, err)
			require.True(t, os.SameFile(oldInfo, newInfo), "premise: and it is the folder of the copy just installed")

			// The second run is the one that used to refuse, and say to remove the only copy.
			out, code = fx.script(t, repoRoot, "install")
			assert.Equal(t, 0, code, out)
			assert.Contains(t, out, "unchanged  codex/alpha")
			assert.NotContains(t, out, "refused")
			out, code = fx.script(t, repoRoot, "check")
			assert.Equal(t, 0, code, out)
			assert.NotContains(t, out, "duplicate")
		})
	}

	// The identity of two folders is decided with test -ef. A sh whose test does not have
	// it would answer "not the same" to everything and bring the message back that says to
	// remove the installed copy, so the script refuses to run. To get an sh like that, the
	// same script is run with -ef spelled as an operator no test has: what such an sh makes
	// of it is an error, not a crash and not a "yes".
	t.Run("an sh whose test has no -ef is refused before anything is read or copied", func(t *testing.T) {
		fx := newSkillsFixture(t)
		src := readFile(t, filepath.Join(repoRoot, "scripts", "skills.sh"))
		without := strings.ReplaceAll(src, `" -ef "`, `" -zzef "`) // the two tests, not the words of the message
		require.NotEqual(t, src, without, "premise: the script uses -ef")
		script := filepath.Join(t.TempDir(), "skills.sh")
		write(t, script, without)
		before := fx.snapshot(t)

		for _, mode := range []string{"check", "install"} {
			cmd := exec.Command("sh", script, mode)
			cmd.Env = append(fx.env(), "SKILLS_DIR="+fx.skills, "BIN_DIR="+fx.bin)
			raw, runErr := cmd.CombinedOutput()
			out := string(raw)
			assert.Equal(t, 1, exitCode(t, runErr), mode+": "+out)
			assert.Contains(t, out, "-ef", mode)
			assert.Contains(t, out, "Nothing was read or copied", mode)
			assert.Equal(t, before, fx.snapshot(t), mode+": nothing was copied")
		}

		out, code := fx.script(t, repoRoot, "install")
		assert.Equal(t, 0, code, "and the script as it is passes in the same fixture: %s", out)
	})
}
