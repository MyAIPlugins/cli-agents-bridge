package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
// precedence) — the entry version is silently ignored". The marketplace entry is
// therefore not a second source, it is a DEAD field, and the cheapest way not to
// have two sources is not to have the second one. It has been removed, and the
// subtest below keeps it removed.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO: it never runs git. A `--depth=1`
// checkout has no tags, and `git describe --tags --always --dirty 2>/dev/null` —
// the Makefile's exact line — does not fail there: it returns a bare SHA that
// looks like a good value. A gate built on it inside ci.yml would compare
// `35df804` against `0.10.0` and be red forever, or be made conditional, and a
// conditional tag check is the silent green we keep closing. The tag is checked
// in exactly one place, release.yml, where it exists by construction.
func TestVersionDrift_EveryMirrorAgreesWithTheSource(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	source := sourceVersion(t, repoRoot)
	readme := readRepoFile(t, repoRoot, "README.md")
	security := readRepoFile(t, repoRoot, "SECURITY.md")

	t.Run("the marketplace entry does not carry a version at all", func(t *testing.T) {
		// Not "carries the same version": carries NONE. A field the runtime
		// ignores cannot be right or wrong, it can only drift, and verifying a
		// value nobody reads is work that buys nothing. Removing it was checked
		// against the validator rather than assumed — `claude plugin validate
		// --strict` passes with the field absent.
		raw := readRepoFile(t, repoRoot, filepath.Join(".claude-plugin", "marketplace.json"))
		var marketplace struct {
			Plugins []map[string]json.RawMessage `json:"plugins"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &marketplace))
		require.NotEmpty(t, marketplace.Plugins, "the marketplace must list the plugin")

		for i, entry := range marketplace.Plugins {
			_, present := entry["version"]
			assert.False(t, present,
				"marketplace.json plugins[%d] declares a version. plugin.json wins at install time "+
					"and this field is silently ignored, so it can only ever go stale: delete it "+
					"rather than update it", i)
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
		// mechanical, by refusing to describe the fix as an edit.
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

	t.Run("the release workflow checks the tag, and reads the source to do it", func(t *testing.T) {
		// The tag half of the guard lives in release.yml because that is where a
		// tag exists. This subtest is what keeps TestVersionDrift_TagMatchesSource
		// from being a skip nobody notices: the local gate proves the wiring, the
		// release run proves the value.
		release := readRepoFile(t, repoRoot, filepath.Join(".github", "workflows", "release.yml"))
		step := stepNamed(t, release, "tag")

		assert.Contains(t, step, "CAB_RELEASE_TAG",
			"the release workflow must hand the tag to the test that compares it")
		assert.Contains(t, step, "github.ref_name",
			"the tag must come from the ref the workflow was triggered by, not from git describe: "+
				"this job is the only place where a tag is guaranteed to exist")
		assert.Contains(t, step, "TestVersionDrift_TagMatchesSource",
			"the step must run the test that does the comparison")
		assert.Empty(t, reSemver.FindAllString(step, -1),
			"no version literal may appear in this step, in code OR in a comment: a number written "+
				"'for the reader' is a second source, and it is the one that goes stale")
	})
}

// TestVersionDrift_TagMatchesSource is the ONE place where the git tag meets the
// manifest, and it runs only in the release workflow.
//
// The skip below is the kind this project distrusts, so it is worth saying why
// it is not a silent green: a skip here is invisible ONLY if the workflow can
// stop setting the variable without anybody noticing, and the subtest above
// asserts that release.yml still sets it. Take that assertion away and this
// becomes exactly the defect it is guarding against.
func TestVersionDrift_TagMatchesSource(t *testing.T) {
	tag, set := os.LookupEnv("CAB_RELEASE_TAG")
	if !set {
		t.Skip("not a release run: CAB_RELEASE_TAG is unset")
	}
	require.NotEmpty(t, tag,
		"CAB_RELEASE_TAG is set but empty: the workflow passed nothing, and an empty tag is a "+
			"failure rather than a reason to skip")

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	assert.Equal(t, sourceVersion(t, repoRoot), strings.TrimPrefix(tag, "v"),
		"the tag being released does not match plugin.json. Releasing anyway ships a plugin whose "+
			"manifest names a different version than the binary next to it — which is how v0.10.0 "+
			"went out with 0.9.0 manifests")
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
