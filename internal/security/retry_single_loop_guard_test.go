package security

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The proof that retry_single_loop_test.go can fail. A guard that has only ever been seen green
// proves nothing, so each way of repeating an operation that it is meant to refuse is written
// down here as a source and must come back red: the loop the rename had before this lot, the loop
// a critic showed a count of doublings would not see (fixed delay, the existing predicate, the
// existing budget and wait), a call from a place nobody listed, a budget written as numbers, a
// call through an aliased import, the loop stored as a value, a new errno predicate, a new wait,
// every spelling of a doubling, and a listed owner that disappears. Each fixture is the REAL tree
// with one edit (an added file, or a text change that is checked to apply), parsed and never
// compiled. The last test is the other half: listing the new place is what makes it green.

func copySources(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func toLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func toCRLF(s string) string { return strings.ReplaceAll(toLF(s), "\n", "\r\n") }

// replaceOnce edits one real file, and fails if the text to change is not there exactly once:
// a fixture whose edit did not apply would be a green test about nothing.
//
// The text a fixture matches is written with \n, and a Windows runner checks the sources out with
// \r\n (git's core.autocrlf): the CI of the lot was red on exactly that, a premise found 0 times
// instead of once. So the file to edit is brought to \n first. Only the fixtures do this: the
// guard on the real tree reads the bytes as they are on disk, and the test below says that its
// verdict does not depend on them.
func replaceOnce(t *testing.T, s map[string]string, file, old, repl string) {
	t.Helper()
	s[file] = toLF(s[file])
	require.Equal(t, 1, strings.Count(s[file], old), "premise of the fixture: %q must appear once in %s", old, file)
	s[file] = strings.Replace(s[file], old, repl, 1)
}

// TestRetry_TheInventoryDoesNotDependOnTheLineEndings: the real tree, once with \n and once with
// \r\n, gives the same sites. The guard parses the sources and looks at names and numbers, never
// at the text between lines, so a checkout with another line ending cannot turn its verdict; this
// test keeps that true on every system and not only on the one that has \r\n on disk.
func TestRetry_TheInventoryDoesNotDependOnTheLineEndings(t *testing.T) {
	onDisk := loadRepoSources(t)
	lf := make(map[string]string, len(onDisk))
	crlf := make(map[string]string, len(onDisk))
	for k, v := range onDisk {
		lf[k] = toLF(v)
		crlf[k] = toCRLF(v)
	}
	require.NotContains(t, lf[rwFile], "\r", "premise: the \\n copy has no carriage return")
	require.Contains(t, crlf[rwFile], "\r\n", "premise: the \\r\\n copy really has them")

	want := scanRetrySources(t, lf)
	require.NotEmpty(t, want, "premise: an empty inventory would be equal to anything")
	assert.Equal(t, want, scanRetrySources(t, crlf))
}

func TestRetry_TheGuardRefusesWhatItIsMeantToRefuse(t *testing.T) {
	cases := []struct {
		name         string
		edit         func(t *testing.T, s map[string]string)
		wantUnlisted []string
		wantStale    []string
	}{
		{
			// The case a count of doublings does not see: another loop, fixed delay, the existing
			// predicate, budget and wait of the rename.
			name: "a second loop with a fixed delay that reuses the predicate, the budget and the wait of the rename",
			edit: func(t *testing.T, s map[string]string) {
				s["internal/transport/fs/another_loop_windows.go"] = `package fs

func anotherLoop(oldpath string, info []byte) error {
	var last error
	for n := 0; n < renameAttempts; n++ {
		_, last = tryReplace(oldpath, info)
		if last == nil || !isTransientReplaceError(last) {
			return last
		}
		if n+1 < renameAttempts {
			renameSleep(renameFirstDelay)
		}
	}
	return last
}
`
			},
			wantUnlisted: []string{
				"vocabulary | internal/transport/fs/another_loop_windows.go::anotherLoop | isTransientReplaceError",
				"vocabulary | internal/transport/fs/another_loop_windows.go::anotherLoop | renameSleep",
				"vocabulary | internal/transport/fs/another_loop_windows.go::anotherLoop | renameAttempts",
				"vocabulary | internal/transport/fs/another_loop_windows.go::anotherLoop | renameFirstDelay",
			},
		},
		{
			// The shape of the rename BEFORE this lot: its own loop, with its own doubling.
			name: "the copy of the loop that the rename had before",
			edit: func(t *testing.T, s map[string]string) {
				s["internal/transport/fs/rename_inline_windows.go"] = `package fs

func renameAtomicTraced(oldpath string, info []byte) error {
	delay := renameFirstDelay
	for attempt := 1; attempt <= renameAttempts; attempt++ {
		_, err := tryReplace(oldpath, info)
		if err == nil || !isTransientReplaceError(err) {
			return err
		}
		if attempt < renameAttempts {
			renameSleep(delay)
			delay *= 2
		}
	}
	return nil
}
`
			},
			wantUnlisted: []string{
				"doubling | internal/transport/fs/rename_inline_windows.go::renameAtomicTraced | delay",
				"vocabulary | internal/transport/fs/rename_inline_windows.go::renameAtomicTraced | isTransientReplaceError",
			},
		},
		{
			name: "the predicate handed to the common loop from a place nobody listed",
			edit: func(t *testing.T, s map[string]string) {
				s["internal/session/another_retry.go"] = `package session

import (
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

func another(op func() error) error {
	last, _ := security.RetryLoop(security.RenameAttempts, security.RenameFirstDelay, time.Sleep, isTransientReplaceError, op)
	return last
}
`
			},
			wantUnlisted: []string{
				"loop-call | internal/session/another_retry.go::another | RetryLoop | security.RenameAttempts, security.RenameFirstDelay, isTransientReplaceError",
				"vocabulary | internal/session/another_retry.go::another | isTransientReplaceError",
				"vocabulary | internal/session/another_retry.go::another | RenameAttempts",
			},
		},
		{
			name: "the same call through an aliased import of this package",
			edit: func(t *testing.T, s map[string]string) {
				s["internal/session/aliased_retry.go"] = `package session

import (
	"time"

	sec "github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

func aliased(op func() error, p func(error) bool) error {
	last, _ := sec.RetryLoop(3, time.Second, time.Sleep, p, op)
	return last
}
`
			},
			wantUnlisted: []string{
				"loop-call | internal/session/aliased_retry.go::aliased | RetryLoop | <literal 3>, time.Second, p",
			},
		},
		{
			name: "a budget written as numbers at the call of the replace",
			edit: func(t *testing.T, s map[string]string) {
				replaceOnce(t, s, rwFile, "security.RetryLoop(renameAttempts, renameFirstDelay,", "security.RetryLoop(6, 10*time.Millisecond,")
			},
			wantUnlisted: []string{"<literal 6>, <expression 10 * time.Millisecond>, isTransientReplaceError"},
			wantStale:    []string{"renameAttempts, renameFirstDelay, isTransientReplaceError"},
		},
		{
			// No name of the vocabulary is involved: only the arguments of the call, as written, see it.
			name: "the replace handed a predicate written inline",
			edit: func(t *testing.T, s map[string]string) {
				replaceOnce(t, s, rwFile, "wait, isTransientReplaceError, func() error {", "wait, func(err error) bool { return err != nil }, func() error {")
			},
			wantUnlisted: []string{"renameAttempts, renameFirstDelay, <expression (func(err error) bool literal)>"},
			wantStale:    []string{"renameAttempts, renameFirstDelay, isTransientReplaceError"},
		},
		{
			name: "the replace wired to the budget of reads and removals",
			edit: func(t *testing.T, s map[string]string) {
				replaceOnce(t, s, rwFile, "security.RetryLoop(renameAttempts, renameFirstDelay,", "security.RetryLoop(renameAttempts, deniedFirstDelay,")
			},
			wantUnlisted: []string{"renameAttempts, deniedFirstDelay, isTransientReplaceError"},
			wantStale:    []string{"renameAttempts, renameFirstDelay, isTransientReplaceError"},
		},
		{
			name: "a budget of the alias written as a number again",
			edit: func(t *testing.T, s map[string]string) {
				replaceOnce(t, s, rwFile, "renameAttempts   = security.RenameAttempts", "renameAttempts   = 6")
			},
			wantStale: []string{"internal/transport/fs/rename_windows.go::const renameAttempts | RenameAttempts"},
		},
		{
			name: "a second use of the wait of the rename inside its own function",
			edit: func(t *testing.T, s map[string]string) {
				replaceOnce(t, s, rwFile, "\t\trenameSleep(d)\n", "\t\trenameSleep(d)\n\t\trenameSleep(d)\n")
			},
			wantUnlisted: []string{"renameRetrying | renameSleep |  (found 2, listed 1)"},
		},
		{
			name: "the loop stored in a variable",
			edit: func(t *testing.T, s map[string]string) {
				s["internal/session/stored_loop.go"] = `package session

import "github.com/myAIPlugins/cli-agents-bridge/internal/security"

var runner = security.RetryLoop
`
			},
			wantUnlisted: []string{"loop-value | internal/session/stored_loop.go::var runner | RetryLoop"},
		},
		{
			name: "a new place that says which Windows refusals are transient",
			edit: func(t *testing.T, s map[string]string) {
				s["internal/session/busy_windows.go"] = `package session

import (
	"errors"

	w "golang.org/x/sys/windows"
)

func isBusy(err error) bool {
	return errors.Is(err, w.ERROR_SHARING_VIOLATION) || errors.Is(err, w.ERROR_ACCESS_DENIED)
}
`
			},
			wantUnlisted: []string{
				"errno | internal/session/busy_windows.go::isBusy | ERROR_SHARING_VIOLATION",
				"errno | internal/session/busy_windows.go::isBusy | ERROR_ACCESS_DENIED",
			},
		},
		{
			name: "a new wait, through an aliased import of time",
			edit: func(t *testing.T, s map[string]string) {
				s["internal/session/poller.go"] = `package session

import tm "time"

func poll() { tm.Sleep(tm.Second) }
`
			},
			wantUnlisted: []string{"sleep | internal/session/poller.go::poll | time.Sleep"},
		},
		{
			name: "a listed owner that is gone",
			edit: func(t *testing.T, s map[string]string) {
				delete(s, rwFile)
			},
			wantStale: []string{
				"loop-call | " + rrOwner + " | RetryLoop",
				"errno | " + rwFile + "::isTransientReplaceError | ERROR_SHARING_VIOLATION",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := copySources(loadRepoSources(t))
			c.edit(t, s)

			unlisted, stale := judgeRetrySites(scanRetrySources(t, s), retryAllowed)

			require.True(t, len(unlisted)+len(stale) > 0, "the guard must be red on this tree")
			for _, w := range c.wantUnlisted {
				assert.True(t, anyContains(unlisted, w), "unlisted must say %q, got:\n%s", w, strings.Join(unlisted, "\n"))
			}
			for _, w := range c.wantStale {
				assert.True(t, anyContains(stale, w), "stale must say %q, got:\n%s", w, strings.Join(stale, "\n"))
			}
		})
	}
}

// Doublings in every spelling the scan knows, each in a new place.
func TestRetry_EveryWayOfDoublingIsSeen(t *testing.T) {
	for _, stmt := range []struct{ name, code string }{
		{"multiply assign", "d *= 2"},
		{"x = x * 2", "d = d * 2"},
		{"x = 2 * x", "d = 2 * d"},
		{"shift assign", "d <<= 1"},
		{"x = x << 1", "d = d << 1"},
		{"add itself", "d += d"},
		{"x = x + x", "d = d + d"},
		{"for post statement", "for i, d := 0, time.Second; i < 3; i, d = i+1, d*2 {\n\t\t_ = d\n\t}\n\treturn 0\n\t//"},
	} {
		t.Run(stmt.name, func(t *testing.T) {
			s := copySources(loadRepoSources(t))
			s["internal/session/backoff_x.go"] = "package session\n\nimport \"time\"\n\nfunc grow(d time.Duration) time.Duration {\n\t" + stmt.code + "\n\treturn d\n}\n"

			unlisted, _ := judgeRetrySites(scanRetrySources(t, s), retryAllowed)

			assert.True(t, anyContains(unlisted, "doubling | internal/session/backoff_x.go::grow | d"), "got:\n%s", strings.Join(unlisted, "\n"))
		})
	}
}

// Listing the new place is what turns it green: the red is a question, and the answer is a line
// with a reason, in the diff.
func TestRetry_ListingANewPlaceIsWhatMakesItGreen(t *testing.T) {
	s := copySources(loadRepoSources(t))
	s["internal/transport/fs/another_loop_windows.go"] = `package fs

func anotherLoop(oldpath string, info []byte) error {
	_, last := tryReplace(oldpath, info)
	if isTransientReplaceError(last) {
		renameSleep(renameFirstDelay)
	}
	return last
}
`
	found := scanRetrySources(t, s)
	unlisted, _ := judgeRetrySites(found, retryAllowed)
	require.NotEmpty(t, unlisted)

	extended := append([]allowedRetrySite{}, retryAllowed...)
	counts := map[retrySite]int{}
	for _, site := range found {
		if strings.HasPrefix(site.owner, "internal/transport/fs/another_loop_windows.go::") {
			counts[site]++
		}
	}
	require.NotEmpty(t, counts)
	for site, n := range counts {
		extended = append(extended, allowedRetrySiteOf(site, n, "the fixture"))
	}

	unlisted, stale := judgeRetrySites(found, extended)

	assert.Empty(t, unlisted)
	assert.Empty(t, stale)
}

func anyContains(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}
