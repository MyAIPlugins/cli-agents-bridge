package integration

import (
	"bytes"
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

const lintGoModBase = "module example.com/lint\n\ngo 1.25.0\n"

type lintSandbox struct {
	dir         string // holds Makefile, .staticcheck-version and go.mod
	bin         string // holds the fake go and the fake staticcheck
	staticcheck string
}

func newLintSandbox(t *testing.T, goMod string) lintSandbox {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	dir, bin := t.TempDir(), t.TempDir()
	for _, name := range []string{"Makefile", ".staticcheck-version"} {
		data, rerr := os.ReadFile(filepath.Join(repoRoot, name))
		require.NoError(t, rerr)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600))

	// The Makefile asks `go env` at parse time (GOEXE, GOBIN, GOPATH): silence is a
	// valid answer for all three. `version` and `vet` say what they were started with.
	fakeGo := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  env) exit 0 ;;\n" +
		"  version) echo \"go version fake $GOTOOLCHAIN\"; exit 0 ;;\n" +
		"  vet) echo \"VET_SAW [$GOTOOLCHAIN]\"; exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0o700))

	pin := strings.TrimPrefix(pinnedVersion(t, repoRoot), "v")
	fakeStaticcheck := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'staticcheck 2026.2.1 (" + pin + ")'; exit 0; fi\n" +
		"echo \"STATICCHECK_SAW [$GOTOOLCHAIN]\"\n"
	staticcheck := filepath.Join(bin, "staticcheck")
	require.NoError(t, os.WriteFile(staticcheck, []byte(fakeStaticcheck), 0o700))

	return lintSandbox{dir: dir, bin: bin, staticcheck: staticcheck}
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

func TestMakefile_LintRunsUnderTheToolchainOfGoMod(t *testing.T) {
	t.Run("the toolchain line is what go vet and staticcheck run under", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, nil)
		require.NoError(t, err, out)
		for _, want := range sees("go1.99.9") {
			assert.Contains(t, out, want)
		}
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
		for _, w := range sees("go1.88.8") {
			assert.Contains(t, out, w)
		}
		assert.NotContains(t, out, "go1.99.9", "the previous value must not be written anywhere in the Makefile")
	})

	// A comment at the end of the line and a commented-out directive are both
	// ordinary in a go.mod. Neither the `go` line nor the commented one may be taken.
	t.Run("a trailing comment and a commented-out line do not count", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\n// toolchain go1.11.1\ntoolchain go1.77.7 // pinned by hand\n")
		out, err := sb.lint(t, nil)
		require.NoError(t, err, out)
		for _, w := range sees("go1.77.7") {
			assert.Contains(t, out, w)
		}
		assert.NotContains(t, out, "go1.11.1")
		assert.NotContains(t, out, "[1.25.0]", "the `go` line is the language minimum, not the toolchain")
	})
}

func TestMakefile_LintToolchainFromTheEnvironmentWins(t *testing.T) {
	t.Run("the environment over go.mod", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, []string{"GOTOOLCHAIN=go1.50.0"})
		require.NoError(t, err, out)
		for _, w := range sees("go1.50.0") {
			assert.Contains(t, out, w)
		}
		assert.NotContains(t, out, "go1.99.9")
	})

	t.Run("the make command line over go.mod", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, nil, "GOTOOLCHAIN=go1.51.0")
		require.NoError(t, err, out)
		for _, w := range sees("go1.51.0") {
			assert.Contains(t, out, w)
		}
		assert.NotContains(t, out, "go1.99.9")
	})

	// Go reads an empty GOTOOLCHAIN as unset, and so does the target: an empty
	// variable left in a shell profile must not turn the gate off.
	t.Run("an empty one counts as unset", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase+"\ntoolchain go1.99.9\n")
		out, err := sb.lint(t, []string{"GOTOOLCHAIN="})
		require.NoError(t, err, out)
		for _, w := range sees("go1.99.9") {
			assert.Contains(t, out, w)
		}
	})

	// The reading of criteria 3 and 4 together: whoever sets GOTOOLCHAIN chooses,
	// so a go.mod without the line is not a reason to refuse.
	t.Run("with the environment set, a go.mod without the line is not a reason to refuse", func(t *testing.T) {
		sb := newLintSandbox(t, lintGoModBase)
		out, err := sb.lint(t, []string{"GOTOOLCHAIN=go1.50.0"})
		require.NoError(t, err, out)
		for _, w := range sees("go1.50.0") {
			assert.Contains(t, out, w)
		}
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
	for _, w := range sees("go1.99.9") {
		assert.Contains(t, out, w)
	}
}
