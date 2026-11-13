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

// F-174: a target that does not use Go does not call it.
//
// The Makefile asked `go env` while it was read (EXE, STATICCHECK_DIR), so with Go off
// the PATH every `make`, `make skills-check` included, printed `make: go: Command not
// found` three times. A fresh agent read it as a broken environment and skipped the
// step. The answers are now asked when a recipe needs them, once.
//
// The property is measured with the fake `go` of fake_tools_test.go, which logs every
// call it receives: "go was not called" is the log being empty, and does not depend on
// whether the machine running the test has a go on its PATH. The positive control is a
// target that does use it: without it an empty log could be a log that cannot see.

type nogoRun struct {
	out   string
	calls []string
}

func nogoMake(t *testing.T, fx skillsFixture, target string, extra ...string) nogoRun {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	log := filepath.Join(t.TempDir(), "calls.log")

	args := append([]string{"--no-print-directory", "-C", repoRoot, target, "SKILLS_DIR=" + fx.skills, "BIN_DIR=" + fx.bin}, extra...)
	cmd := exec.Command("make", args...)
	cmd.Env = append(fx.env(), "PATH="+fakeTools(t)+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_TOOL_LOG="+log)
	out, _ := cmd.CombinedOutput() // the exit code is not what is measured here: skills-check exits 1 on a copy that differs

	data, rerr := os.ReadFile(log)
	if rerr != nil {
		require.ErrorIs(t, rerr, os.ErrNotExist, "no log file means the fake was never called")
		return nogoRun{out: string(out)}
	}
	return nogoRun{out: string(out), calls: strings.Split(strings.TrimSpace(string(data)), "\n")}
}

func (r nogoRun) callsTo(prefix string) int {
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestMakefile_TargetsThatDoNotUseGoDoNotCallIt(t *testing.T) {
	fx := newSkillsFixture(t)

	// Each target with a word that only its own run prints: the premise that the target
	// ran, and not a `make` that stopped at the first line.
	for _, tc := range []struct{ target, ran string }{
		{"skills-check", "claude/alpha"},
		{"install-skills", "claude/alpha"},
		{"help", "skills-check"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			r := nogoMake(t, fx, tc.target)
			require.Contains(t, r.out, tc.ran, "premise: the target ran")
			assert.Empty(t, r.calls, "go was called by a target that has no use for it")
		})
	}
}

// The control, and the other half of the contract: go IS asked when a recipe needs the
// answer, and only once for each question however many lines use it.
func TestMakefile_GoIsAskedWhenARecipeNeedsItAndOnlyOnce(t *testing.T) {
	fx := newSkillsFixture(t)

	t.Run("build asks for the executable suffix once", func(t *testing.T) {
		r := nogoMake(t, fx, "build", "-n") // -n: the recipe is expanded and printed, not run
		require.Contains(t, r.out, "go build", "premise: the recipe was expanded")
		assert.Equal(t, 1, r.callsTo("go env GOEXE"), "once, though two lines of the recipe use it: %v", r.calls)
	})

	t.Run("lint asks where go install puts staticcheck once", func(t *testing.T) {
		r := nogoMake(t, fx, "lint", "-n")
		require.Contains(t, r.out, "staticcheck", "premise: the recipe was expanded")
		assert.Equal(t, 1, r.callsTo("go env GOBIN"), "once, though the path and the message both use it: %v", r.calls)
	})
}
