package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// releaseTagEnv is the ONE spelling of the variable that carries the tag from
// the release workflow into the guard. It is used by the guard's own LookupEnv
// and by the workflow contract below, so the two cannot drift onto different
// names: renaming it here changes what the contract expects, and a workflow
// still saying the old name goes red.
const releaseTagEnv = "CAB_RELEASE_TAG"

// THE PRODUCT VERSION IS WRITTEN IN ONE PLACE AND MIRRORED IN FOUR, and until
// this file nothing checked that the mirrors still said what the source says.
//
// The defect is not hypothetical and it is not symmetric. When v0.10.0 was
// tagged, both plugin manifests stayed at 0.9.0 and an agent from another team
// noticed — not a test. That much was fixed by hand; what stayed open was the
// MECHANISM, and by the time this file was written the manifests were the only
// two places telling the truth: README and SECURITY.md still announced v0.9.0
// in six lines, four of them normative and two of them commands a user runs.
//
// Which is the part worth remembering: a check built on "the two manifests"
// would have been GREEN on a repository that declared the wrong version in six
// other places. The number of places is not two, and a guard has to know that.
//
// WHY plugin.json is the source, rather than a file we elected: it is what the
// runtime reads. `claude plugin validate --strict` on a disagreeing pair says so
// in its own words — "At install time, plugin.json wins (calculatePluginVersion
// precedence) — the entry version is silently ignored".
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO: it never runs git. A `--depth=1`
// checkout has no tags, and `git describe --tags --always --dirty 2>/dev/null` —
// the Makefile's exact line — does not fail there: it returns a bare SHA that
// looks like a good value. A gate built on it inside ci.yml would compare a SHA
// against a semver and be red forever, or be made conditional, and a conditional
// tag check is the silent green we keep closing. The tag is checked in exactly
// one place, release.yml, where it exists by construction.
func TestVersionDrift_EveryMirrorAgreesWithTheSource(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	source := sourceVersion(t, repoRoot)
	readme := readRepoFile(t, repoRoot, "README.md")
	security := readRepoFile(t, repoRoot, "SECURITY.md")

	t.Run("the marketplace entry does not carry a version at all", func(t *testing.T) {
		// Removed rather than checked — but NOT because the field is inert in
		// general, which is what an earlier version of this comment claimed. It
		// is the FALLBACK the runtime uses when plugin.json declares no version:
		// "plugin.json wins" names a precedence, and a precedence has a loser
		// only because there is something to lose to.
		//
		// What makes it removable HERE is that plugin.json always declares one,
		// so the entry can never be consulted and can only ever drift. Measured
		// rather than reasoned: with the field absent `claude plugin validate
		// --strict` passes, and with plugin.json's version absent instead the
		// validator still demands it FROM plugin.json — so whatever the fallback
		// does at install time, it does not satisfy validation.
		raw := readRepoFile(t, repoRoot, filepath.Join(".claude-plugin", "marketplace.json"))
		var marketplace struct {
			Plugins []map[string]json.RawMessage `json:"plugins"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &marketplace))
		require.NotEmpty(t, marketplace.Plugins, "the marketplace must list the plugin")

		for i, entry := range marketplace.Plugins {
			_, present := entry["version"]
			assert.False(t, present,
				"marketplace.json plugins[%d] declares a version. plugin.json declares one too and "+
					"wins at install time, so this copy is never read and can only go stale: delete "+
					"it rather than update it", i)
		}
	})

	t.Run("the README status lines mirror the source", func(t *testing.T) {
		assert.Equal(t, source,
			soleCapture(t, readme, reStatusLine, "README.md **Status** line"),
			"README.md announces a version the plugin manifest does not")
		assert.Equal(t, source,
			soleCapture(t, readme, reCurrentEntry, `README.md changelog entry marked "(current)"`),
			`the README entry marked "(current)" is not the current version`)
	})

	t.Run("the README install blocks carry no version literal", func(t *testing.T) {
		// These two lines are the only ones in the repository that a reader
		// EXECUTES. `.goreleaser.yml` names archives cab-bridge_{{ .Version }}_…,
		// so a stale number here does not merely misinform: it unpacks a file the
		// reader never downloaded.
		//
		// They carry a placeholder instead of a number because their neighbours
		// already do — OS=darwin # darwin | linux, ARCH=arm64 # arm64 | amd64 —
		// so two of the three values were already substituted by hand. Aligning
		// the third removes the only one of the three that can go false on its
		// own. This is the "remove a place rather than verify it" half of the fix.
		for _, probe := range []struct {
			re   *regexp.Regexp
			what string
		}{
			{reShellVersionAssign, "README.md macOS/Linux install block (VERSION=)"},
			{rePowerShellVersionAssign, "README.md Windows install block ($Version =)"},
		} {
			value := soleCapture(t, readme, probe.re, probe.what)
			assert.NotRegexp(t, reSemver, value,
				"%s names a version literal (%q). A copyable command that hard-codes the release "+
					"is wrong for every release but one — use a placeholder", probe.what, value)
		}
	})

	t.Run("SECURITY.md names the release its controls were re-read against", func(t *testing.T) {
		// THIS ONE IS NOT A MIRROR, AND THE FAILURE MESSAGE HAS TO SAY SO.
		//
		// "current through vX" is not a staleness marker: it states how far
		// somebody actually checked the described controls against the code — the
		// document says as much ("verified against the code at each release rather
		// than assumed"). So the red here is not "a number needs updating". It is
		// "this release has not been re-read yet".
		//
		// The cheapest way to make this test green is a sed on the digits, and
		// that would certify work that did not happen. Nothing in a test can stop
		// that; what a test CAN do is make the lie deliberate instead of
		// mechanical, by refusing to describe the fix as an edit. It earned its
		// keep on first contact: the re-read it forced found that v0.10.0 had in
		// fact changed internal/security, which the document was about to deny.
		const meaning = "\n\n" +
			"    This is not a stale number to bump. SECURITY.md claims its controls were verified\n" +
			"    against the code AT THIS RELEASE. Changing this value DECLARES that you re-read\n" +
			"    internal/security against the shipped binary for %s.\n" +
			"    If you have not done that, the red is correct — leave it red."

		assert.Equal(t, source,
			soleCapture(t, security, reSecurityHeader, "SECURITY.md header"),
			"SECURITY.md covers a release that is no longer the current one"+meaning, source)
		assert.Equal(t, source,
			soleCapture(t, security, reSecurityHonestyNote, "SECURITY.md honesty note"),
			"the SECURITY.md honesty note covers a release that is no longer the current one"+meaning, source)
	})

	t.Run("the release workflow matches its contract, byte for byte", func(t *testing.T) {
		// THE OBJECT UNDER CONTRACT IS THE WHOLE FILE, not a step found by name,
		// and that is the entire point of this subtest.
		//
		// The first version asserted things ABOUT a step: that it named the env
		// variable, that it ran the guard, that it carried no version literal.
		// Every one of those passed while six different edits disarmed the guard
		// completely — commenting the env line out, moving the env onto a later
		// step, adding continue-on-error, reordering past goreleaser, wrapping the
		// command so it cannot fail, renaming the Go function the YAML names. Six
		// occurrences of one shape are not six bugs to patch: they are the shape
		// being wrong. An exact comparison of the whole file makes five of them
		// visible without a technique of its own, because order, guards and shell
		// text are all properties an exact form pins down.
		//
		// The sixth needs the other half, below: the names are DERIVED from the
		// symbols instead of retyped, so a rename either propagates or goes red.
		contract := readRepoFile(t, repoRoot,
			filepath.Join("tests", "integration", "testdata", "release.yml.contract"))
		actual := readRepoFile(t, repoRoot, filepath.Join(".github", "workflows", "release.yml"))

		expected := strings.NewReplacer(
			"@@ENV@@", releaseTagEnv,
			"@@TEST@@", tagGuardFuncName(t),
		).Replace(contract)

		// CRLF only. Nothing else is normalised: no per-line trimming, no comment
		// stripping. Each of those would reintroduce a way to change behaviour
		// without changing what the test compares.
		assert.Equal(t, normalizeEOL(expected), normalizeEOL(actual),
			"\n.github/workflows/release.yml differs from its contract.\n\n"+
				"    THIS IS NOT A YAML ERROR AND NOT A FORMATTING NIT — read it as an alarm on the\n"+
				"    contract itself. The whole file is compared because five of the six known ways to\n"+
				"    disarm the tag guard leave a step that still READS correctly: a comment character\n"+
				"    on the env line, the env moved to a later step, continue-on-error, a reordering\n"+
				"    past goreleaser, a command wrapped so it cannot fail.\n\n"+
				"    A red here means one of two things and you have to decide WHICH:\n"+
				"      - the workflow changed on purpose: review the diff above, and then update\n"+
				"        tests/integration/testdata/release.yml.contract deliberately;\n"+
				"      - the workflow changed by accident: this is the guard doing its job.\n\n"+
				"    Do not make it green by relaxing the comparison. A gate somebody switches off\n"+
				"    is not a gate, and this one exists because the previous, looser assertions were\n"+
				"    green through all six mutations.")
	})
}

// TestVersionDrift_TagMatchesSource is the ONE place where the git tag meets the
// manifest, and it runs only in the release workflow.
//
// The skip below is the kind this project distrusts, so it is worth saying what
// keeps it honest, because "the subtest checks the workflow mentions it" was NOT
// enough: a mention proves a string, not an execution. What proves the execution
// is TestVersionDrift_TheTagGuardRunsAndCanFail, which re-runs this very function
// in a child process and requires it to PASS on a coherent manifest and FAIL on a
// mismatched one.
func TestVersionDrift_TagMatchesSource(t *testing.T) {
	tag, set := os.LookupEnv(releaseTagEnv)
	if !set {
		t.Skipf("not a release run: %s is unset", releaseTagEnv)
	}
	require.NotEmptyf(t, tag,
		"%s is set but empty: the workflow passed nothing, and an empty tag is a failure rather "+
			"than a reason to skip", releaseTagEnv)

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	assert.Equal(t, sourceVersion(t, repoRoot), strings.TrimPrefix(tag, "v"),
		"the tag being released does not match plugin.json. Releasing anyway ships a plugin whose "+
			"manifest names a different version than the binary next to it — which is how v0.10.0 "+
			"went out with 0.9.0 manifests")
}

// TestVersionDrift_TheTagGuardRunsAndCanFail EXECUTES the guard instead of
// reading about it.
//
// The distinction is the whole lesson of this lot. `go test -run` on a name that
// does not exist prints "ok … [no tests to run]" and exits 0 — a command that
// runs, does nothing, and says so in a note nobody reads, while the exit code
// says the opposite. So "the workflow mentions the test" and "the test skipped"
// and "the test ran and passed" are three different worlds that an exit code
// alone cannot tell apart. This one asks for the verdict, not the status.
func TestVersionDrift_TheTagGuardRunsAndCanFail(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err, "the running test binary must be re-executable to prove the guard runs")
	packageDir, err := os.Getwd()
	require.NoError(t, err)

	name := tagGuardFuncName(t)
	source := sourceVersion(t, mustAbs(t, "../.."))

	// No shell anywhere in here: the child is exec'd with an argv, so nothing in
	// a tag value can be interpreted on the way.
	run := func(t *testing.T, value string, set bool) (string, int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		// -test.count=1, and note WHY it is not what it looks like. It is the
		// binary's own flag (testing.go registers "test.count", default 1), passed
		// explicitly so the child does not inherit a default somebody changes
		// later. It is NOT a defence against the test result cache: that cache
		// belongs to `go test`, and this child is the compiled binary executed
		// directly, so nothing here could ever be served from it. The first draft
		// of this comment claimed the cache reason — a true flag with a false
		// justification, which is the exact class this file exists to close.
		cmd := exec.CommandContext(ctx, self, "-test.run=^"+name+"$", "-test.v", "-test.count=1")
		cmd.Dir = packageDir
		// REPLACED, not appended to: a duplicate key leaves the winner up to the
		// platform, and the whole point is to control what the child sees.
		env := envWithout(os.Environ(), releaseTagEnv)
		if set {
			env = append(env, releaseTagEnv+"="+value)
		}
		cmd.Env = env

		out, runErr := cmd.CombinedOutput()
		require.NotErrorIs(t, ctx.Err(), context.DeadlineExceeded,
			"the child guard did not finish in time")
		code := 0
		if runErr != nil {
			var exit *exec.ExitError
			require.ErrorAs(t, runErr, &exit, "the child failed for a reason other than a test verdict")
			code = exit.ExitCode()
		}
		return clip(string(out)), code
	}

	t.Run("a coherent manifest makes it PASS, and it really ran", func(t *testing.T) {
		out, code := run(t, "v"+source, true)
		assert.Equal(t, 0, code, "the guard must accept a tag that matches plugin.json:\n%s", out)
		// An exit code is not the verdict. These three make the difference between
		// "passed", "skipped" and "never selected" impossible to confuse.
		assert.Contains(t, out, "--- PASS: "+name, "the guard must have RUN and passed:\n%s", out)
		assert.NotContains(t, out, "--- SKIP", "the guard skipped while the variable was set:\n%s", out)
		assert.NotContains(t, out, "no tests to run",
			"the child selected nothing — the derived name does not match any test:\n%s", out)
	})

	t.Run("a mismatched tag makes it FAIL, with a diagnosis", func(t *testing.T) {
		out, code := run(t, "v99.99.99", true)
		assert.NotEqual(t, 0, code, "a tag that disagrees with plugin.json must not pass:\n%s", out)
		assert.Contains(t, out, "--- FAIL: "+name, "it must fail as a verdict, not crash:\n%s", out)
		assert.Contains(t, out, "does not match plugin.json",
			"the failure must say what disagreed, not merely that something did:\n%s", out)
	})

	t.Run("an empty variable FAILS rather than skipping", func(t *testing.T) {
		out, code := run(t, "", true)
		assert.NotEqual(t, 0, code, "an empty tag must be a failure:\n%s", out)
		assert.Contains(t, out, "--- FAIL: "+name, "an empty tag must fail, not skip:\n%s", out)
	})
}

// tagGuardFuncName derives the guard's name FROM THE SYMBOL, so that the YAML
// and the Go declaration cannot drift apart silently.
//
// The parameter is typed `func(*testing.T)` on purpose: the call site hands over
// the function itself, so renaming only the declaration does not compile. That
// is the half a string comparison can never have — and the defect it closes is
// not exotic, it is a rename, which is the most ordinary edit there is.
func tagGuardFuncName(t *testing.T) string {
	t.Helper()
	return derivedFuncName(t, TestVersionDrift_TagMatchesSource)
}

func derivedFuncName(t *testing.T, f func(*testing.T)) string {
	t.Helper()
	fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer())
	require.NotNil(t, fn, "the function symbol must be resolvable: without it there is no name to "+
		"compare and the contract would silently check a placeholder")

	full := fn.Name() // e.g. github.com/…/tests/integration.TestVersionDrift_TagMatchesSource
	short := full[strings.LastIndex(full, ".")+1:]
	require.Regexp(t, `^Test[A-Z_][A-Za-z0-9_]*$`, short,
		"derived %q from %q, which is not a plain test-function name. A closure or a method would "+
			"yield something like func1, and feeding that to -test.run selects nothing while "+
			"exiting 0", short, full)
	return short
}

// sourceVersion reads the single place that declares the product version.
func sourceVersion(t *testing.T, repoRoot string) string {
	t.Helper()
	raw := readRepoFile(t, repoRoot,
		filepath.Join("plugins", "cli-agents-bridge", ".claude-plugin", "plugin.json"))
	var manifest struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &manifest))
	require.Regexp(t, `^\d+\.\d+\.\d+$`, manifest.Version,
		"plugin.json must carry a bare semver: it is what every other place is checked against")
	return manifest.Version
}

func readRepoFile(t *testing.T, repoRoot, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, rel))
	require.NoError(t, err, "%s must exist: a guard whose subject is missing is a guard that "+
		"stopped guarding", rel)
	return string(raw)
}

func mustAbs(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(rel)
	require.NoError(t, err)
	return abs
}

func normalizeEOL(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// envWithout returns env with every entry for key removed, so the caller can set
// it exactly once rather than shadowing an inherited one.
func envWithout(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// clip bounds a child's output so a failure message stays readable. The head is
// what carries the verdict lines.
func clip(s string) string {
	const max = 4000
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… (output clipped)"
}

// soleCapture returns the first capture group of the ONE line matching re.
//
// Zero matches and two matches are both failures, and that is the whole point of
// the helper. A probe that no longer recognises its subject reports nothing to
// compare and would otherwise pass — which is how a `sed` that stopped matching
// let a whole line through and made a gate recommend installing the binary it
// had just refused. If a document gets rewritten, this test must go red so the
// probe is fixed; it must never quietly start checking nothing.
func soleCapture(t *testing.T, text string, re *regexp.Regexp, what string) string {
	t.Helper()
	matches := re.FindAllStringSubmatch(text, -1)
	require.Len(t, matches, 1,
		"%s: expected exactly one line matching %s, found %d. If the document was rewritten, "+
			"repair THIS probe — do not delete it: with no subject it checks nothing and stays green",
		what, re, len(matches))
	return matches[0][1]
}

// The probes are anchored to markers rather than to line numbers, and each
// marker was verified to occur exactly once in its file. Scoping matters here:
// README.md contains ten semvers and six of them are legitimate history, so a
// blanket ban on version literals would be unworkable — an assertion that cries
// about the wrong line gets switched off.
var (
	reSemver       = regexp.MustCompile(`\d+\.\d+\.\d+`)
	reStatusLine   = regexp.MustCompile(`(?m)^\*\*Status\*\*: v(\d+\.\d+\.\d+)`)
	reCurrentEntry = regexp.MustCompile(`(?m)^- \*\*v(\d+\.\d+\.\d+)\*\* \(current\)`)

	reShellVersionAssign      = regexp.MustCompile(`(?m)^VERSION=(.*)$`)
	rePowerShellVersionAssign = regexp.MustCompile(`(?m)^\$Version\s*=\s*(.*)$`)

	reSecurityHeader      = regexp.MustCompile(`current through \*\*v(\d+\.\d+\.\d+)\*\*`)
	reSecurityHonestyNote = regexp.MustCompile(`Honesty note \(through v(\d+\.\d+\.\d+)\)`)
)
