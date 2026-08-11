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

// TestMakefile_LintLooksWhereGoInstallPuts is the regression for a remediation
// that could not fix what it diagnosed.
//
// `make lint` requires staticcheck and prints an install command when it is
// missing. The first version resolved the binary in $PATH and $GOPATH/bin only
// — but `go install` writes to $GOBIN whenever that is set, so on such a machine
// the gate looked where the binary is not, and then told the reader to install
// it there again. Fail-closed, so nothing unsafe was authorised: a deterministic
// false failure with a remediation that loops.
//
// Third instance of that class in one day, after a repair command that repaired
// a different project and an error telling a peer to quote something while
// showing an unquotable form. A suggested command is a claim like any other, and
// the way to check it is to run it and see whether the problem goes away.
//
// The assertion is on WHICH binary is chosen, not on the exit code: a gate that
// happens to fail for the right reason today is not the property being pinned.
func TestMakefile_LintLooksWhereGoInstallPuts(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	// A fake staticcheck ONLY under GOBIN, with GOPATH pointing somewhere that
	// has none — so the two candidate directories give different answers and the
	// test can tell which one the Makefile used.
	tmp := t.TempDir()
	gobin := filepath.Join(tmp, "gobin")
	gopath := filepath.Join(tmp, "gopath")
	require.NoError(t, os.MkdirAll(gobin, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(gopath, "bin"), 0o700))
	fake := filepath.Join(gobin, "staticcheck")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\necho FAKE_STATICCHECK\n"), 0o700))

	run := func(env ...string) string {
		t.Helper()
		cmd := exec.Command("make", "-n", "lint")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), env...)
		out, _ := cmd.CombinedOutput() // -n prints, it does not run: exit code is not the subject
		return string(out)
	}

	t.Run("GOBIN set wins, because that is where go install writes", func(t *testing.T) {
		out := run("GOBIN="+gobin, "GOPATH="+gopath)
		assert.Contains(t, out, fake,
			"with GOBIN set the gate must look there — otherwise its own install command cannot fix it")
		assert.NotContains(t, out, filepath.Join(gopath, "bin", "staticcheck"),
			"and must not fall back to GOPATH/bin, which is empty on such a machine")
	})

	t.Run("GOBIN empty falls back to GOPATH/bin", func(t *testing.T) {
		out := run("GOBIN=", "GOPATH="+gopath)
		assert.Contains(t, out, filepath.Join(gopath, "bin", "staticcheck"),
			"the ordinary machine, and the behaviour that must not regress")
	})

	t.Run("the pinned version is one file, shared with CI", func(t *testing.T) {
		// The Makefile's install hint and the CI workflow must not drift onto two
		// different linters; both read .staticcheck-version.
		raw, rerr := os.ReadFile(filepath.Join(repoRoot, ".staticcheck-version"))
		require.NoError(t, rerr, "the single source of the pin must exist")
		version := strings.TrimSpace(string(raw))
		require.NotEmpty(t, version)

		ci, rerr := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
		require.NoError(t, rerr)
		assert.Contains(t, string(ci), ".staticcheck-version",
			"CI must READ the file rather than repeat the number, or the two drift apart in silence")
		assert.NotContains(t, string(ci), "staticcheck@v",
			"a literal version in the workflow is the drift this file exists to prevent")
	})
}
