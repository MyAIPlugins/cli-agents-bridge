package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-159: `make lint` runs under the toolchain go.mod names in its `toolchain`
// line, so the local gate and the CI gate are the same object.
//
// The tests plant a fake `go` and a fake staticcheck that report the GOTOOLCHAIN
// they were started with, in a directory that holds a COPY of the Makefile next
// to a go.mod of the test's choosing. The property is what the two commands
// SEE, not what `make -n` prints: choosing a toolchain is not running under it.
// The real run, with the machine's default Go, is the gate of the lot.
//
// The fakes are compiled programs (fake_tools_test.go), and every call they
// receive is logged: the tests check that the fake answered the commands that
// matter, and not only that the output has the words it should. A real go that
// answers instead of the fake is a failure of the test, whatever it prints.

const lintGoModBase = "module example.com/lint\n\ngo 1.25.0\n"

type lintSandbox struct {
	dir         string // holds Makefile, .staticcheck-version and go.mod
	bin         string // holds the fake go and the fake staticcheck, shared by the run
	staticcheck string
	log         string // every call the fakes received, one line each
	pin         string // the version the fake staticcheck reports
}

func newLintSandbox(t *testing.T, goMod string) lintSandbox {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	dir, bin := t.TempDir(), fakeTools(t)
	for _, name := range []string{"Makefile", ".staticcheck-version"} {
		data, rerr := os.ReadFile(filepath.Join(repoRoot, name))
		require.NoError(t, rerr)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600))

	return lintSandbox{
		dir:         dir,
		bin:         bin,
		staticcheck: filepath.Join(bin, "staticcheck"+binExeSuffix),
		log:         filepath.Join(t.TempDir(), "calls.log"),
		pin:         strings.TrimPrefix(pinnedVersion(t, repoRoot), "v"),
	}
}

// lint runs `make lint` in the sandbox. GOTOOLCHAIN is removed from the inherited
// environment first, so what the test sets is the only thing that can be seen.
func (s lintSandbox) lint(t *testing.T, env []string, makeArgs ...string) (string, error) {
	t.Helper()
	args := append([]string{"lint", "STATICCHECK=" + s.staticcheck}, makeArgs...)
	cmd := exec.Command("make", args...)
	cmd.Dir = s.dir
	cmd.Env = envWithout(os.Environ(), "GOTOOLCHAIN")
	cmd.Env = append(cmd.Env, "PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, "FAKE_TOOL_LOG="+s.log, "FAKE_STATICCHECK_PIN="+s.pin)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return clip(string(out)), err
}

func sees(toolchain string) []string {
	return []string{
		"VET_SAW [" + toolchain + "]",
		"STATICCHECK_SAW [" + toolchain + "]",
		"go version fake " + toolchain,
	}
}

// calls returns the calls the fakes logged, in order. A log that does not exist
// means no fake was called at all.
func (s lintSandbox) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	text := strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n"))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// assertRanUnder is what "the gate ran under this toolchain" means: the output
// has the lines only the fakes print, AND the fakes were the ones called. The
// first alone can be true while a real go answered half of the commands, which is
// what Windows did (see fake_tools_test.go): the log is what says it did not.
// The `go env` of the Makefile's parse time must have reached the fake too; the
// toolchain is checked only on the three calls that run inside the target.
func (s lintSandbox) assertRanUnder(t *testing.T, out, toolchain string) {
	t.Helper()
	for _, want := range sees(toolchain) {
		assert.Contains(t, out, want)
	}
	calls := s.calls(t)
	parseTime := 0
	for _, call := range calls {
		if strings.HasPrefix(call, "go env ") {
			parseTime++
		}
	}
	assert.Positive(t, parseTime, "the go the Makefile asks at parse time must be the fake: %v", calls)
	for _, want := range []string{
		"go version [" + toolchain + "]",
		"go vet ./... [" + toolchain + "]",
		"staticcheck ./... [" + toolchain + "]",
	} {
		assert.Contains(t, calls, want, "the fakes' call log")
	}
}

func TestMakefile_LintRunsUnderTheToolchainOfGoMod(t *testing.T) {
	t.Run("the toolchain line is what go vet and staticcheck run under", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, nil)
		require.NoError(t, err, out)
		sb.assertRanUnder(t, out, "go1.99.9")
	})

	// The version has ONE source. Same Makefile bytes, another line in go.mod,
	// another toolchain seen by both commands.
	t.Run("another line, the same Makefile, another toolchain", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.88.8\n")
		repoRoot, err := filepath.Abs("../..")
		require.NoError(t, err)
		want, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(sb.dir, "Makefile"))
		require.NoError(t, err)
		require.True(t, bytes.Equal(want, got), "premise: the Makefile under test is the repository's, untouched")

		out, err := sb.lint(t, nil)
		require.NoError(t, err, out)
		sb.assertRanUnder(t, out, "go1.88.8")
		assert.NotContains(t, out, "go1.99.9", "the previous value must not be written anywhere in the Makefile")
	})

	// A comment at the end of the line and a commented-out directive are both
	// ordinary in a go.mod. Neither the `go` line nor the commented one may be taken.
	t.Run("a trailing comment and a commented-out line do not count", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\n// toolchain go1.11.1\ntoolchain go1.77.7 // pinned by hand\n")
		out, err := sb.lint(t, nil)
		require.NoError(t, err, out)
		sb.assertRanUnder(t, out, "go1.77.7")
		assert.NotContains(t, out, "go1.11.1")
		assert.NotContains(t, out, "[1.25.0]", "the `go` line is the language minimum, not the toolchain")
	})
}

func TestMakefile_LintToolchainFromTheEnvironmentWins(t *testing.T) {
	t.Run("the environment over go.mod", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, []string{"GOTOOLCHAIN=go1.50.0"})
		require.NoError(t, err, out)
		sb.assertRanUnder(t, out, "go1.50.0")
		assert.NotContains(t, out, "go1.99.9")
	})

	t.Run("the make command line over go.mod", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, nil, "GOTOOLCHAIN=go1.51.0")
		require.NoError(t, err, out)
		sb.assertRanUnder(t, out, "go1.51.0")
		assert.NotContains(t, out, "go1.99.9")
	})

	// Go reads an empty GOTOOLCHAIN as unset, and so does the target: an empty
	// variable left in a shell profile must not turn the gate off.
	t.Run("an empty one counts as unset", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, []string{"GOTOOLCHAIN="})
		require.NoError(t, err, out)
		sb.assertRanUnder(t, out, "go1.99.9")
	})

	// The reading of criteria 3 and 4 together: whoever sets GOTOOLCHAIN chooses,
	// so a go.mod without the line is not a reason to refuse.
	t.Run("with the environment set, a go.mod without the line is not a reason to refuse", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase)
		out, err := sb.lint(t, []string{"GOTOOLCHAIN=go1.50.0"})
		require.NoError(t, err, out)
		sb.assertRanUnder(t, out, "go1.50.0")
	})
}

func TestMakefile_LintWithoutAToolchainLineRefuses(t *testing.T) {
	sb := newLintSandbox(t, lintGoModBase)

	out, err := sb.lint(t, nil)
	require.Error(t, err, "no toolchain line and no GOTOOLCHAIN: the gate must not pick a version by itself: %s", out)
	assert.Contains(t, out, "go.mod has no 'toolchain' line")
	assert.Contains(t, out, "go mod edit -toolchain=")
	for _, ran := range []string{"VET_SAW", "STATICCHECK_SAW", "go version fake"} {
		assert.NotContains(t, out, ran, "nothing may have run before the refusal")
	}
	// The log says it with no dependence on what a command prints: the only calls
	// that may precede the refusal are the Makefile's own questions to go at parse
	// time.
	for _, call := range sb.calls(t) {
		assert.True(t, strings.HasPrefix(call, "go env "), "only the parse-time `go env` may have run before the refusal, not: %s", call)
	}

	// The command it prints is a claim like any other: run it, with the real go,
	// and see whether the refusal goes away. (-toolchain only edits the file; it
	// downloads nothing, and GOTOOLCHAIN=local keeps the real go from switching.)
	edit := exec.Command("go", "mod", "edit", "-toolchain=go1.99.9")
	edit.Dir = sb.dir
	edit.Env = append(envWithout(os.Environ(), "GOTOOLCHAIN"), "GOTOOLCHAIN=local")
	editOut, editErr := edit.CombinedOutput()
	require.NoError(t, editErr, "the remediation the refusal prints must work: %s", editOut)

	out, err = sb.lint(t, nil)
	require.NoError(t, err, "and after it the gate runs: %s", out)
	sb.assertRanUnder(t, out, "go1.99.9")
}
