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

// F-174, the finding of the gate of the diff: what `go env` answers is DATA, and it has to
// reach the recipe as the bytes Go printed.
//
// The lazy variables were `X = $(eval X := $(shell go env ...))$(X)`. make expands the
// argument of `eval` first, so the answer of Go was pasted INTO the text `X := <answer>`
// and then read again as Makefile source: a `#` in GOBIN began a comment and cut the path,
// and a `$` was expanded as a variable. A GOBIN of `.../bin#suffix` made `make lint` refuse
// a staticcheck that was sitting in that very directory, and its message told the reader
// to install the tool that was already there. The base did not do this: `:=` stores what
// the shell printed. The fix defers the `$(shell)` to the assignment that eval reads
// (`$$(shell ...)`), which stores the output as it is.
//
// Measured with the fake go of fake_tools_test.go, which answers `go env <NAME>` with what
// the test puts in FAKE_GOENV_<NAME>, and with the real `make`. The `make lint` here is
// run, not printed: a `-n` would show a path and not what the target does with it.

// goAnswerEnv is the environment of a run in which the only go is the fake and no
// staticcheck is reachable through PATH, so that the only way for the gate to find one is
// the directory the fake go announced.
func goAnswerEnv(t *testing.T, s lintSandbox, extra ...string) []string {
	t.Helper()
	onlyGo := t.TempDir()
	name := "go" + binExeSuffix
	data, err := os.ReadFile(filepath.Join(s.bin, name))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(onlyGo, name), data, 0o700))

	env := os.Environ()
	for _, key := range []string{"GOTOOLCHAIN", "GOBIN", "GOPATH", "PATH"} {
		env = envWithout(env, key)
	}
	env = append(env,
		"PATH="+onlyGo+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin",
		"FAKE_TOOL_LOG="+s.log, "FAKE_STATICCHECK_PIN="+s.pin)
	env = append(env, extra...)

	// The premise, asserted: a staticcheck in PATH would find the gate a tool whatever the
	// variable said, and the test would be green for a reason that is not the one it claims.
	probe := exec.Command("sh", "-c", "command -v staticcheck")
	probe.Env = env
	require.Error(t, probe.Run(), "premise: with this PATH no staticcheck is reachable")
	return env
}

// plantStaticcheck puts the fake staticcheck in dir and returns it.
func plantStaticcheck(t *testing.T, s lintSandbox, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	name := "staticcheck" + binExeSuffix
	data, err := os.ReadFile(filepath.Join(s.bin, name))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o700))
}

func goAnswerSandbox(t *testing.T) lintSandbox {
	t.Helper()
	return newLintSandbox(t, lintGoModBase+"toolchain go1.27.1\n")
}

func TestMakefile_LintFindsStaticcheckWhereGoSaysWhateverTheCharacters(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, s lintSandbox, root string) []string // the FAKE_GOENV_* answers
	}{
		{"GOBIN with a hash", func(t *testing.T, s lintSandbox, root string) []string {
			dir := filepath.Join(root, "bin#suffix")
			plantStaticcheck(t, s, dir)
			return []string{"FAKE_GOENV_GOBIN=" + dir}
		}},
		{"GOBIN with a space and a hash", func(t *testing.T, s lintSandbox, root string) []string {
			dir := filepath.Join(root, "my bin#suffix")
			plantStaticcheck(t, s, dir)
			return []string{"FAKE_GOENV_GOBIN=" + dir}
		}},
		{"GOPATH with a hash, GOBIN empty", func(t *testing.T, s lintSandbox, root string) []string {
			gopath := filepath.Join(root, "go#path")
			plantStaticcheck(t, s, filepath.Join(gopath, "bin"))
			return []string{"FAKE_GOENV_GOPATH=" + gopath}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := goAnswerSandbox(t)
			env := goAnswerEnv(t, s, tc.setup(t, s, t.TempDir())...)

			cmd := exec.Command("make", "lint")
			cmd.Dir = s.dir
			cmd.Env = env
			out, err := cmd.CombinedOutput()

			require.Contains(t, string(out), "VET_SAW", "premise: the target ran up to the staticcheck lookup")
			require.NoError(t, err, "the staticcheck sits in the directory go announced:\n%s", clip(string(out)))
			assert.Contains(t, string(out), "STATICCHECK_SAW", "and it is the one that ran")
		})
	}

	// The control, and the second thing the finding showed: when the tool really is not
	// there, the refusal names the directory it looked in WHOLE. A message that names a
	// truncated path sends the reader to install the tool into a directory that is not
	// the one `go install` writes to.
	t.Run("the refusal names the whole directory it looked in", func(t *testing.T) {
		s := goAnswerSandbox(t)
		dir := filepath.Join(t.TempDir(), "my bin#suffix")
		require.NoError(t, os.MkdirAll(dir, 0o700)) // there, and empty
		env := goAnswerEnv(t, s, "FAKE_GOENV_GOBIN="+dir)

		cmd := exec.Command("make", "lint")
		cmd.Dir = s.dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()

		require.Error(t, err, "premise: with no staticcheck anywhere the target refuses")
		assert.Contains(t, string(out), "staticcheck not found (looked in "+dir+",", "the whole directory, hash and space included")
	})
}

// EXE and BINARY are lazy the same way, and EXE is what `go env GOEXE` answers: `.exe` on
// Windows, empty elsewhere, so a `#` or a space there is not a case a machine will produce.
// It is the same mechanism as the directory above, and the property is the mechanism's:
// whatever Go prints reaches the recipe untouched, as one value and not as words.
func TestMakefile_ExecutableSuffixIsTakenAsGoPrintsIt(t *testing.T) {
	s := goAnswerSandbox(t)
	env := goAnswerEnv(t, s, "FAKE_GOENV_GOEXE=.e #x")

	cmd := exec.Command("make", "-n", "build")
	cmd.Dir = s.dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()

	require.NoError(t, err, clip(string(out)))
	text := string(out)
	require.Contains(t, text, "go build", "premise: the recipe was expanded")
	assert.Contains(t, text, `-o "bin/cab-bridge.e #x"`, "BINARY is the name and the suffix Go printed, whole")
	assert.Contains(t, text, "built: bin/cab-bridge.e #x", "and so is the line that announces it")
}
