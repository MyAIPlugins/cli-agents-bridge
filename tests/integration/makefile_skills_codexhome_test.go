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

// F-174, the second point: where Codex is on a machine is CODEX_HOME when it is set, and
// ~/.codex when it is not. Codex's own code reads it that way (an empty value counts as
// not set), and so does what the script says about it: "Codex is here" is a folder that
// exists, and the old skills folder Codex still reads is `skills` inside it.
//
// What does not move is where the copies go: ~/.agents/skills is under HOME whatever
// CODEX_HOME says, in Codex's own list of the places it reads the user's skills.
//
// Before, the script knew only ~/.codex: with Codex living elsewhere and no ~/.codex,
// check and install skipped Codex with a line that was true ("~/.codex does not exist")
// and did not say that Codex was somewhere else.
//
// The fixture takes CODEX_HOME out of the environment it passes on (fx.env), so what a
// test sets is the only thing the script can see.
func TestSkills_CodexHomeIsWhereCodexIs(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	// withoutDotCodex is a fixture whose ~/.codex is gone, and a folder elsewhere that
	// stands for Codex's home.
	withoutDotCodex := func(t *testing.T) (skillsFixture, string) {
		t.Helper()
		fx := newSkillsFixture(t)
		require.NoError(t, os.RemoveAll(filepath.Join(fx.home, ".codex")))
		codexHome := filepath.Join(t.TempDir(), "codex elsewhere")
		require.NoError(t, os.MkdirAll(codexHome, 0o700))
		require.NoDirExists(t, filepath.Join(fx.home, ".codex"), "premise: no ~/.codex")
		return fx, codexHome
	}

	t.Run("Codex is here when CODEX_HOME exists, with no ~/.codex", func(t *testing.T) {
		fx, codexHome := withoutDotCodex(t)

		out, code := fx.script(t, repoRoot, "check", "CODEX_HOME="+codexHome)
		assert.Equal(t, 1, code, "check exits 1 on a copy that is missing: "+out)
		assert.NotContains(t, out, "skipped    codex/", "Codex is not skipped")
		assert.Contains(t, out, "missing    codex/alpha")

		out, code = fx.script(t, repoRoot, "install", "CODEX_HOME="+codexHome)
		require.Equal(t, 0, code, out)
		assert.Contains(t, out, "installed  codex/alpha")
		assert.FileExists(t, fx.agentsSkills("alpha", "SKILL.md"), "the copy goes to ~/.agents/skills, under HOME")
		assert.NoDirExists(t, filepath.Join(codexHome, "skills"), "and nothing is put in Codex's home")
		assert.NoDirExists(t, filepath.Join(fx.home, ".codex"), "nor is ~/.codex created")
	})

	t.Run("CODEX_HOME that does not exist: skipped, and the line names the folder it looked at", func(t *testing.T) {
		fx := newSkillsFixture(t) // here ~/.codex exists, and CODEX_HOME says Codex is not there
		require.DirExists(t, filepath.Join(fx.home, ".codex"), "premise: ~/.codex is there")
		gone := filepath.Join(t.TempDir(), "not a folder")

		out, code := fx.script(t, repoRoot, "check", "CODEX_HOME="+gone)
		assert.Equal(t, 1, code, out) // claude and antigravity are missing too: only Codex is asserted below
		var line string
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "skipped    codex/alpha") {
				line = l
			}
		}
		require.NotEmpty(t, line, "Codex is skipped: "+out)
		assert.Contains(t, line, gone, "the line names CODEX_HOME, where it looked")
		assert.NotContains(t, line, filepath.Join(fx.home, ".codex"), "and not the folder it did not look at")
	})

	t.Run("an empty CODEX_HOME is as if it were not set", func(t *testing.T) {
		fx := newSkillsFixture(t)

		out, code := fx.script(t, repoRoot, "install", "CODEX_HOME=")
		require.Equal(t, 0, code, out)
		assert.Contains(t, out, "installed  codex/alpha")
	})

	// The old folder follows Codex's home: that is the one Codex still reads.
	t.Run("a copy in skills/ under CODEX_HOME is the old folder: refused", func(t *testing.T) {
		fx, codexHome := withoutDotCodex(t)
		write(t, filepath.Join(codexHome, "skills", "alpha", "SKILL.md"), "an old alpha\n")

		out, code := fx.script(t, repoRoot, "check", "CODEX_HOME="+codexHome)
		assert.Contains(t, out, "duplicate  codex/alpha", out)
		assert.Equal(t, 1, code)

		out, code = fx.script(t, repoRoot, "install", "CODEX_HOME="+codexHome)
		assert.NotEqual(t, 0, code, "the install refuses: "+out)
		assert.NoFileExists(t, fx.agentsSkills("alpha", "SKILL.md"), "and installs nothing")
		// What the refusal tells the user to clean is the folder Codex reads.
		assert.Contains(t, out, "move it out of "+filepath.Join(codexHome, "skills"))
		assert.NotContains(t, out, "~/.codex/skills", "it does not send the reader to a folder Codex does not read")
	})

	t.Run("without CODEX_HOME the refusal names ~/.codex/skills, as it always did", func(t *testing.T) {
		fx := newSkillsFixture(t)
		write(t, fx.oldCopy("alpha", "SKILL.md"), "an old alpha\n")

		out, code := fx.script(t, repoRoot, "install")
		assert.NotEqual(t, 0, code, "the install refuses: "+out)
		assert.Contains(t, out, "move it out of ~/.codex/skills, then run again")
	})

	t.Run("a copy in ~/.codex/skills is not the old folder when Codex is elsewhere", func(t *testing.T) {
		fx, codexHome := withoutDotCodex(t)
		write(t, fx.oldCopy("alpha", "SKILL.md"), "an alpha in a folder Codex does not read\n")

		out, _ := fx.script(t, repoRoot, "check", "CODEX_HOME="+codexHome)
		assert.NotContains(t, out, "duplicate  codex/alpha", "Codex does not read ~/.codex/skills now: "+out)
		assert.Contains(t, out, "missing    codex/alpha")
	})
}
