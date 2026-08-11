package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	t.Run("the pinned version is one file, and NOTHING repeats the number", func(t *testing.T) {
		// The first version of this subtest asserted that CI *reads* the file and
		// that no `staticcheck@v...` literal appears on a command line. Both were
		// true, and the property claimed by the commit — one source — was still
		// false: the number was also sitting in a COMMENT in the workflow.
		//
		// The critic proved it the only way that works for a "single source":
		// change the value in the declared source and see what stays behind. The
		// subtest was green with the file saying v9.9.9 and the workflow still
		// announcing the old number. Proving a mechanism EXISTS does not prove it
		// is the only one, and for a single source it is the absence of
		// alternatives that has to be proven.
		raw, rerr := os.ReadFile(filepath.Join(repoRoot, ".staticcheck-version"))
		require.NoError(t, rerr, "the single source of the pin must exist")
		version := strings.TrimSpace(string(raw))
		require.Regexp(t, `^v\d+\.\d+\.\d+$`, version)

		ci, rerr := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
		require.NoError(t, rerr)
		assert.Contains(t, string(ci), ".staticcheck-version",
			"CI must READ the file rather than repeat the number")

		// ANY semver literal in the staticcheck STEP — comments included, since a
		// comment is exactly where the second copy hid. Scoped to the step rather
		// than the whole file: the first draft of this assertion swept the entire
		// Makefile and tripped on `git checkout v0.2.3`, a project tag in an
		// unrelated example. A regression that cries about the wrong line gets
		// switched off, so it has to name its subject.
		semver := regexp.MustCompile(`v\d+\.\d+\.\d+`)
		assert.Empty(t, semver.FindAllString(stepNamed(t, string(ci), "staticcheck"), -1),
			"no version literal may appear in the staticcheck step, in code OR in a comment: "+
				"a number written 'for the reader' is a second source, and it is the one that goes stale")

		// Same rule on the Makefile, restricted to the lines that talk about it.
		mk, rerr := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
		require.NoError(t, rerr)
		for _, line := range strings.Split(string(mk), "\n") {
			if strings.Contains(strings.ToLower(line), "staticcheck") {
				assert.Empty(t, semver.FindAllString(line, -1),
					"the Makefile must derive the version, never restate it: %q", line)
			}
		}

		// LIMIT, stated rather than left to be assumed: this catches a literal in
		// the places the number has actually appeared. A semver written somewhere
		// that never mentions staticcheck would still slip through — the honest
		// guard against that is the critic's method, which is to change the value
		// in the source and see what stays behind.
	})

	// Resolving the right PATH is not the same as running the right binary: the
	// gate executed whatever it found. An outdated staticcheck then produced a
	// false green, a newer one a false red, while the commit claimed the version
	// was pinned in one place (CRI).
	t.Run("a staticcheck whose version is not the pinned one is REFUSED", func(t *testing.T) {
		pin := pinnedVersion(t, repoRoot)
		// The wrong versions are DERIVED from the pin rather than written down, so
		// they cannot accidentally become the pin the day someone bumps the file —
		// which would turn "refused" into "runs" and the test into a no-op.
		notThePin := pin + "1"
		for _, tc := range []struct {
			name, reports string
			wantRun       bool
		}{
			{"the pinned version runs", "staticcheck 2026.1 (" + pin + ")", true},
			{"a different one is refused", "staticcheck 2024.1 (" + notThePin + ")", false},
			{"one that cannot say is refused", "", false},
			{"one that answers nonsense is refused", "not a version at all", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				fake := filepath.Join(dir, "staticcheck")
				script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + tc.reports + "'; exit 0; fi\necho FAKE_LINT_RAN\n"
				require.NoError(t, os.WriteFile(fake, []byte(strings.ReplaceAll(script, "\\n", "\n")), 0o700))

				cmd := exec.Command("make", "lint", "STATICCHECK="+fake)
				cmd.Dir = repoRoot
				out, err := cmd.CombinedOutput()

				if tc.wantRun {
					// BOTH, and the exit code is not decoration: today the marker is
					// printed by the last recipe line, so a failure after it cannot
					// exist — add one recipe below staticcheck and this case would go
					// green on a failing `make lint`, with its own name saying the
					// opposite (CRI, P3).
					require.NoError(t, err, "the pinned version must run AND the target must succeed: %s", out)
					assert.Contains(t, string(out), "FAKE_LINT_RAN", "the pinned version must be executed")
					return
				}
				require.Error(t, err, "a mismatched staticcheck must not be run: %s", out)
				assert.Contains(t, string(out), "version mismatch")
				assert.NotContains(t, string(out), "FAKE_LINT_RAN",
					"and must not have been executed before the check")
			})
		}
	})
}

// TestMakefile_LintRunsFromAPathWithSpaces is F-124's own defect, found inside
// the Makefile written to verify F-124's fix.
//
// The existence guard quoted "$(STATICCHECK)"; the version probe and the
// invocation did not. So a GOBIN whose basename contains a space split the path:
// the correct binary — reporting exactly the pinned version — was never invoked,
// the gate read "unknown", refused it, and then advised reinstalling it in the
// same directory. The remediation loop, on another component of the path, two
// hours after closing it elsewhere.
//
// The assertion is EXIT 0 PLUS THE MARKER, not that `make -n` prints the path:
// choosing a binary is not running it, which is the distinction this whole lot
// keeps turning on.
func TestMakefile_LintRunsFromAPathWithSpaces(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	pin := pinnedVersion(t, repoRoot)

	// A space in the DIRECTORY, which is the half a quoted $(STATICCHECK) in one
	// place out of three did not survive.
	gobin := filepath.Join(t.TempDir(), "cri staticcheck space")
	require.NoError(t, os.MkdirAll(gobin, 0o700))
	fake := filepath.Join(gobin, "staticcheck")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'staticcheck 2026.1 (" + pin + ")'; exit 0; fi\n" +
		"echo MARKER_LINT_RAN\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o700))

	cmd := exec.Command("make", "lint")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOBIN="+gobin, "PATH=/usr/bin:/bin:"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()

	require.NoError(t, err, "a correct staticcheck under a path with a space must not be refused: %s", out)
	assert.Contains(t, string(out), "MARKER_LINT_RAN", "and it must actually be RUN, not merely resolved")
	assert.NotContains(t, string(out), "unknown",
		"the version probe must reach the binary too — reading 'unknown' here is the path having been split")
	assert.NotContains(t, string(out), "version mismatch",
		"refusing the right binary and then advising to reinstall it in the same place is the loop we keep closing")
}

func pinnedVersion(t *testing.T, repoRoot string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".staticcheck-version"))
	require.NoError(t, err)
	return strings.TrimSpace(string(raw))
}

// stepNamed returns the YAML block of a workflow step, from its `- name: <n>`
// line to the next step at the same indentation — so an assertion about "the
// staticcheck step" is about that step and not about the whole file.
func stepNamed(t *testing.T, yaml, name string) string {
	t.Helper()
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "- name: ") && strings.Contains(l, name) {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "step %q not found in the workflow", name)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- name: ") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}
