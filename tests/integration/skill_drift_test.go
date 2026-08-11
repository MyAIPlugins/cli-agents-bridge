package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSkillDrift_PublicSkillNamesThePastableField is a DRIFT TRIPWIRE, and that
// classification is the point rather than a caveat.
//
// WHAT IT CANNOT DO: prove that an agent uses the field as a single argv. That
// the name appears in a file does not show how anything behaves — it is
// presence → absence-of-problems, the exact inference this project has been
// catching all day, and writing it down here is cheaper than rediscovering it.
// The behavioural proof is internal/shellarg's test, which runs a real shell;
// the proof of what an agent actually LOADED is the fresh-agent black box, which
// is lot 3.
//
// WHAT IT DOES DO: fail when the code and the shipped instruction drift apart.
// That happened for eight hours today — one skill said "copy it", another said
// "always between quotes", and nobody compared them because each had been
// checked on its own. This is the only link of that chain a test can hold today,
// because this skill is versioned WITH the code; the personal one is not, and no
// gate can reach it.
//
// Both directions, deliberately: the new instruction must be present AND the old
// one absent. Checking only presence would stay green with two contradictory
// sentences in the same file, which is the state we are leaving.
func TestSkillDrift_PublicSkillNamesThePastableField(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	path := filepath.Join(repoRoot, "plugins", "cli-agents-bridge", "skills", "bridge-workflow", "SKILL.md")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the shipped skill must exist: it is the copy users install")
	skill := string(raw)

	t.Run("it names the field that is safe to paste", func(t *testing.T) {
		assert.Contains(t, skill, "fromAddressShellArg",
			"the skill must name the pastable field, or agents keep pasting the logical one")
	})

	t.Run("it does not tell the reader to add quotes", func(t *testing.T) {
		// The instruction that was WRONG, not merely superseded: "always between
		// quotes" holds for a space and breaks on an apostrophe, because the
		// reader closes the quote they opened. If it comes back, this fails.
		for _, banned := range []string{"always between quotes", "sempre fra apici", "always in quotes"} {
			assert.NotContains(t, strings.ToLower(skill), strings.ToLower(banned),
				"the skill must not ask the reader to quote: that rule is wrong on `Alan's Project`")
		}
	})

	t.Run("the field it names exists in the code", func(t *testing.T) {
		// Without this the tripwire could pass on a field we renamed or never
		// shipped — a skill agreeing with itself about something absent.
		next, rerr := os.ReadFile(filepath.Join(repoRoot, "cmd", "cab-bridge", "next.go"))
		require.NoError(t, rerr)
		assert.Contains(t, string(next), `json:"fromAddressShellArg,omitempty"`,
			"the name in the skill must be the name on the wire")
	})
}
