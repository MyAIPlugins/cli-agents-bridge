//go:build !windows

package fs

import (
	"os"
	"testing"
)

// assertPOSIXPerm asserts the POSIX permission bits of a file the code under
// test created. The Windows counterpart in posixperm_windows_test.go says why
// there is nothing to assert there, and says it out loud instead of quietly
// passing.
func assertPOSIXPerm(t *testing.T, got, want os.FileMode, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %04o, want %04o", what, got, want)
	}
}
