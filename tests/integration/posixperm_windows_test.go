//go:build windows

package integration

import (
	"os"
	"testing"
)

// assertPOSIXPerm asserts NOTHING on Windows, and the reason is not that it is
// hard: Windows has no POSIX permission bits. Go reports 0777 for every
// directory and 0666 for every file, so the comparison could only ever be
// against a constant that describes the platform rather than the code — a green
// assertion that verifies nothing.
//
// What restricts the data dir here are the ACLs inherited from %USERPROFILE%.
// That is a DIFFERENT claim from the one these call sites make, and writing an
// ACL check under this name would silently turn a mode test into a security
// test that nobody reviewed as one. It belongs in SECURITY.md as a declared
// under-claim, and it is Alan's decision, not this lot's.
//
// The hole is LOGGED rather than left silent: `go test -v` on Windows names
// every assertion that did not happen, so nobody has to infer it from a build
// tag. The rest of each test still runs — the file must exist, be a directory,
// carry the right content — which is most of what these tests are for.
func assertPOSIXPerm(t *testing.T, got, want os.FileMode, what string) {
	t.Helper()
	t.Logf("windows: %s NOT asserted (wanted POSIX %04o, the platform reports %04o) — see posixperm_windows_test.go", what, want, got)
}
