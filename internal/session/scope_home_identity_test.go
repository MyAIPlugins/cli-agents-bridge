package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE $HOME EXCLUSION WAS A BYTE COMPARISON ON A PATH NOBODY HAD CANONICALISED.
//
// FindProjectRoot refuses to treat $HOME as a project root even when it holds a
// dotfiles `.git`, because otherwise every marker-less project under $HOME would
// collapse onto one shared scope. The guard was `dir != cleanHome` — and by
// contract the canonicalisation is the CALLER's job, so the walk routinely sees
// a spelling of $HOME that is not byte-equal to the one it was handed.
//
// Four spellings reach it, and none of them needs a case-insensitive volume: a
// symlinked home (either direction), /tmp against /private/tmp, Unicode NFC vs
// NFD, and macOS firmlinks (/Users/x and /System/Volumes/Data/Users/x share a
// (dev,ino) and EvalSymlinks keeps them distinct, because a firmlink is not a
// symlink). With any of them the guard silently stops guarding: two unrelated
// projects both resolve to $HOME, see each other as peers, and exchange
// messages — the isolation fails OPEN.
//
// The tests below are why the guard looked covered: every pre-existing one
// passed `home` and `cwd` in the SAME spelling, which is the one case where a
// byte comparison is right.

// twoSpellingsOfSameDirectory returns two lexically different paths that are the
// same directory on disk. On macOS t.TempDir() is already under /var/folders,
// an alias of /private/var/folders, so the pair costs nothing; elsewhere it is
// built with a symlink.
//
// It verifies with os.SameFile that the two really are one directory before
// handing them back: a fixture whose premise does not hold measures nothing, and
// would do it while staying green.
func twoSpellingsOfSameDirectory(t *testing.T) (aliased, canonical string) {
	t.Helper()

	canonical = t.TempDir()
	aliased = alternateSpellingOf(t, canonical)
	requireSameDirectory(t, aliased, canonical)
	require.NotEqual(t, aliased, canonical, "the two spellings must differ lexically")
	return aliased, canonical
}

// alternateSpellingOf returns a second path naming dir, lexically different from
// it. On macOS EvalSymlinks already yields one (/var vs /private/var); elsewhere
// a symlink is planted OUTSIDE dir so the caller can still reach it after making
// dir itself unreadable.
//
// A trailing separator or an ordinary ".." would NOT do: filepath.Clean folds
// both, so they never reach the comparison. Using one produced a test that
// looked like it exercised the guard and did not — caught because it failed for
// the opposite reason to the one expected.
func alternateSpellingOf(t *testing.T, dir string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		return resolved
	}
	link := filepath.Join(t.TempDir(), "spelling-"+filepath.Base(dir))
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("this platform offers neither an aliased temp dir nor symlinks: %v", err)
	}
	return link
}

func requireSameDirectory(t *testing.T, a, b string) {
	t.Helper()
	infoA, err := os.Stat(a)
	require.NoError(t, err)
	infoB, err := os.Stat(b)
	require.NoError(t, err)
	require.True(t, os.SameFile(infoA, infoB),
		"fixture premise broken: %q and %q are not the same directory, so this test would "+
			"pass without exercising anything", a, b)
}

// homeWithDotfilesAndTwoProjects lays out the shape the guard exists for: a
// dotfiles repository in $HOME, and two marker-less projects under it that must
// stay isolated from each other.
func homeWithDotfilesAndTwoProjects(t *testing.T, home string) (one, two string) {
	t.Helper()
	mkGitDir(t, home)
	one = filepath.Join(home, "one")
	two = filepath.Join(home, "two")
	require.NoError(t, os.MkdirAll(one, 0o700))
	require.NoError(t, os.MkdirAll(two, 0o700))
	return one, two
}

// TestFindProjectRoot_HomeUnderAnotherSpelling_StaysExcluded is the regression.
// Without the identity check it FAILS: both projects resolve to the home
// directory and become one scope.
func TestFindProjectRoot_HomeUnderAnotherSpelling_StaysExcluded(t *testing.T) {
	t.Parallel()
	aliased, canonical := twoSpellingsOfSameDirectory(t)

	for _, tc := range []struct {
		name       string
		walkedFrom string // the spelling the cwd is under
		knownHome  string // the spelling the caller passes as home
	}{
		{"cwd aliased, home canonical", aliased, canonical},
		{"cwd canonical, home aliased", canonical, aliased},
	} {
		t.Run(tc.name, func(t *testing.T) {
			one, two := homeWithDotfilesAndTwoProjects(t, tc.walkedFrom)

			scopeOne, err := FindProjectRoot(one, tc.knownHome)
			require.NoError(t, err)
			scopeTwo, err := FindProjectRoot(two, tc.knownHome)
			require.NoError(t, err)

			assert.NotEqual(t, scopeOne, scopeTwo,
				"two marker-less projects under $HOME collapsed onto one scope because $HOME was "+
					"spelled differently (%q vs %q). They would see each other as peers and exchange "+
					"messages: the isolation fails OPEN, not closed", tc.walkedFrom, tc.knownHome)
			assert.Equal(t, one, scopeOne, "each project must be its own scope")
			assert.Equal(t, two, scopeTwo, "each project must be its own scope")
		})
	}
}

// TestFindProjectRoot_HomeUnderAnotherSpelling_CaseAndUnicode covers the two
// spellings that need no link at all. Both are skipped where the filesystem does
// not conflate them — and the skip is honest because the premise is CHECKED with
// os.SameFile rather than assumed from the operating system: case sensitivity is
// a property of the volume, not of the OS.
func TestFindProjectRoot_HomeUnderAnotherSpelling_CaseAndUnicode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, made, referredTo string
	}{
		{"case", "Alpha", "alpha"},
		// "cafe" + combining acute (NFD) against the precomposed form (NFC).
		{"unicode NFC/NFD", "café", "café"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			made := filepath.Join(base, tc.made)
			referred := filepath.Join(base, tc.referredTo)
			require.NoError(t, os.MkdirAll(made, 0o700))

			madeInfo, err := os.Stat(made)
			require.NoError(t, err)
			referredInfo, err := os.Stat(referred)
			if err != nil || !os.SameFile(madeInfo, referredInfo) {
				t.Skipf("this volume keeps %q and %q distinct, so the spelling cannot collide here",
					tc.made, tc.referredTo)
			}

			one, two := homeWithDotfilesAndTwoProjects(t, made)
			scopeOne, err := FindProjectRoot(one, referred)
			require.NoError(t, err)
			scopeTwo, err := FindProjectRoot(two, referred)
			require.NoError(t, err)

			assert.NotEqual(t, scopeOne, scopeTwo,
				"$HOME written as %q was not recognised when handed in as %q", tc.made, tc.referredTo)
		})
	}
}

// dataVolumePrefix is where macOS mounts the data volume that the system's
// firmlinks point into. It is a fixed property of the platform since Catalina,
// not a path of any particular machine — everything else below is derived from
// the fixture.
const dataVolumePrefix = "/System/Volumes/Data"

// TestFindProjectRoot_HomeUnderAnotherSpelling_Firmlink covers the fourth
// spelling, and it is the one worth having a test for rather than a sentence:
// a firmlink is NOT a symlink, so EvalSymlinks does not reduce the two names to
// one, and a caller that canonicalises cannot make them converge. It exists on
// every macOS since Catalina.
//
// The second spelling is derived from the REALPATH, not from the temp path: the
// firmlinks are on /private and /Users, while /var is an ordinary symlink to
// /private/var and carries none. Prefixing the temp path directly yields a
// directory that does not exist — a probe that would have skipped forever while
// looking like coverage.
func TestFindProjectRoot_HomeUnderAnotherSpelling_Firmlink(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Skipf("cannot resolve the fixture to a real path: %v", err)
	}
	viaDataVolume := filepath.Join(dataVolumePrefix, real)

	realInfo, realErr := os.Stat(real)
	dataInfo, dataErr := os.Stat(viaDataVolume)
	if realErr != nil || dataErr != nil || !os.SameFile(realInfo, dataInfo) {
		t.Skipf("no firmlink on this system: %q is not the same directory as %q", viaDataVolume, real)
	}
	require.NotEqual(t, viaDataVolume, real, "the two spellings must differ lexically")

	// The property that makes this case distinct from a symlink, asserted rather
	// than assumed: canonicalising does NOT collapse the two, so the caller
	// cannot hand the walk a single form even if it wanted to.
	// The two RESOLVED forms are compared, not "does the data-volume path come
	// back byte-identical": what the case needs is that canonicalising leaves
	// them DISTINCT, and a future change to normalisation could alter how each is
	// spelled without bringing them together. Asserting the stricter thing would
	// fail on a change that costs the test nothing.
	resolvedData, err := filepath.EvalSymlinks(viaDataVolume)
	require.NoError(t, err)
	resolvedReal, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)
	require.NotEqual(t, resolvedReal, resolvedData,
		"canonicalising brought the two spellings together, so a caller could hand the walk a "+
			"single form: this would be the symlink case under another name rather than a firmlink")

	one, two := homeWithDotfilesAndTwoProjects(t, real)
	scopeOne, err := FindProjectRoot(one, viaDataVolume)
	require.NoError(t, err)
	scopeTwo, err := FindProjectRoot(two, viaDataVolume)
	require.NoError(t, err)

	assert.NotEqual(t, scopeOne, scopeTwo,
		"$HOME reached through the data-volume firmlink was not recognised as $HOME, so two "+
			"marker-less projects under it collapsed onto one scope")
}

// TestFindProjectRoot_UnverifiableHome_KeepsPreviousBehaviour is the test that
// separates the two candidate error policies, and it is the reason the other one
// was rejected.
//
// "unverifiable => fall back to abs" reads prudent and breaks everything: with
// HOME=/nonexistent — a container, `sudo -H`, CI with a synthetic user — the
// stat fails on EVERY join, so pairing collapses in every repository, including
// the ones that have nothing to do with $HOME. That trades a defect with a rare
// precondition for a regression with a common one.
//
// So: with no proof of identity we preserve the previous behaviour and accept
// the marker. This test is RED under the rejected policy and green under this
// one.
func TestFindProjectRoot_UnverifiableHome_KeepsPreviousBehaviour(t *testing.T) {
	t.Parallel()
	repo := filepath.Join(t.TempDir(), "repo")
	mkGitDir(t, repo)
	nested := filepath.Join(repo, "internal", "session")
	require.NoError(t, os.MkdirAll(nested, 0o700))

	missingHome := filepath.Join(t.TempDir(), "there-is-no-such-home")
	_, err := os.Stat(missingHome)
	require.True(t, os.IsNotExist(err), "the fixture needs a home that really is not there")

	got, err := FindProjectRoot(nested, missingHome)
	require.NoError(t, err)
	assert.Equal(t, repo, got,
		"a home that cannot be stat'd must not cost every unrelated repository its scope")
}

// TestFindProjectRoot_HomeUnreadable_DocumentsTheOpenBranch records a limit
// rather than a guarantee, and it is written down BECAUSE it is inconvenient.
//
// When $HOME is reached through a directory that cannot be traversed, the stat
// fails, the policy above accepts the marker, and the two projects collapse
// exactly as they did before the fix. The identity check narrows the defect to
// the cases where identity is verifiable; it does not close the permission/IO
// branch. Anyone reading this test should read it as the documented edge, not as
// a passing guard.
func TestFindProjectRoot_HomeUnreadable_DocumentsTheOpenBranch(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny traversal")
	}

	// TWO ROUTES TO ONE DIRECTORY, and only one of them is blocked. That shape is
	// required, not decorative: locking the directory that CONTAINS the marker
	// hides the marker too, the walk finds nothing, and the projects stay apart
	// for the wrong reason — a test that would have passed while exercising
	// nothing. So the home directory stays reachable for the walk, and it is the
	// SPELLING handed to the caller that goes through the locked door.
	base := t.TempDir()
	home := filepath.Join(base, "home")
	require.NoError(t, os.MkdirAll(home, 0o700))
	one, two := homeWithDotfilesAndTwoProjects(t, home)

	locked := filepath.Join(base, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o700))
	spelling := filepath.Join(locked, "same-home")
	if err := os.Symlink(home, spelling); err != nil {
		t.Skipf("cannot build a second route to the home directory: %v", err)
	}
	requireSameDirectory(t, spelling, home)

	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	// THE PREMISE IS PINNED, not hoped for. Asking only that the stat FAIL would
	// let a later change to this fixture — dropping the symlink, say — turn the
	// branch into a plain ENOENT, which is UnverifiableHome's case rather than
	// this one. The test would then be a duplicate of another and would never
	// say so.
	_, statErr := os.Stat(spelling)
	if statErr == nil {
		t.Skip("this platform still stats through a 0000 directory")
	}
	require.True(t, os.IsPermission(statErr),
		"this test is about the PERMISSION branch specifically; the stat failed with %v", statErr)
	require.DirExists(t, filepath.Join(home, ".git"),
		"the marker must remain readable: hiding it too would separate the projects for the wrong "+
			"reason, and this test would pass while exercising nothing")
	require.DirExists(t, one, "the walk's own route must stay reachable")

	// Home handed in under a spelling the byte comparison cannot match, so only
	// the identity check could have caught it — and it cannot, because it has no
	// proof to work with.
	scopeOne, err := FindProjectRoot(one, spelling)
	require.NoError(t, err)
	scopeTwo, err := FindProjectRoot(two, spelling)
	require.NoError(t, err)

	// The exact scopes, not merely "the same as each other": two projects that
	// both fell back to their own directory would also be equal to nothing in
	// particular, and that is a different outcome from collapsing onto $HOME.
	assert.Equal(t, home, scopeOne, "DOCUMENTED LIMIT: the collapse target is $HOME itself")
	assert.Equal(t, home, scopeTwo, "DOCUMENTED LIMIT: the collapse target is $HOME itself")
	assert.Equal(t, scopeOne, scopeTwo,
		"DOCUMENTED LIMIT: with the configured spelling of $HOME unreadable the identity check has "+
			"no proof, keeps the previous behaviour, and the two projects collapse onto one scope.\n"+
			"    If this assertion fails, RE-EXAMINE — do not assume an improvement. The documented "+
			"behaviour changed, and the cause may equally be a regression elsewhere or this fixture "+
			"no longer building the permission branch. Verify isolation and pairing before promoting "+
			"this test to a guarantee.")
}

// TestFindProjectRoot_DifferentDirectoriesStayDifferent guards the branch next
// door to the one above: the identity check must not start conflating genuinely
// distinct directories that merely look alike.
func TestFindProjectRoot_DifferentDirectoriesStayDifferent(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	notHome := filepath.Join(base, "home2")
	require.NoError(t, os.MkdirAll(home, 0o700))
	mkGitDir(t, notHome)
	nested := filepath.Join(notHome, "sub")
	require.NoError(t, os.MkdirAll(nested, 0o700))

	got, err := FindProjectRoot(nested, home)
	require.NoError(t, err)
	assert.Equal(t, notHome, got,
		"a repository that is not $HOME must keep its own scope: the identity check narrows the "+
			"exclusion, it must not widen it")
}
