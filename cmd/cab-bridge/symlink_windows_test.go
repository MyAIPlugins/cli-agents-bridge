//go:build windows

package main

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// mustSymlink creates a symlink a fixture needs, or SKIPS the test BY NAME when
// this host will not create one.
//
// Windows grants SeCreateSymbolicLinkPrivilege only to an elevated process or
// with Developer Mode on. Where it is granted — this PC, and the elevated
// windows-latest runner in CI — the link is created and the test runs FOR REAL,
// which is the whole point: a test that asserts an exclusion has to run where
// the thing being excluded can exist. A fixture that silently produced no
// symlink would leave the refusal untested and the result green.
//
// Where it is not granted, the skip names the missing privilege. That is the
// difference between "this passed" and "this was never attempted", and it is
// the only difference a reader of the output can act on.
//
// Only ERROR_PRIVILEGE_NOT_HELD skips. Any other failure is a failure: a
// missing target, a bad path, a full disk have nothing to do with privileges,
// and swallowing them under the same skip would hide real breakage behind a
// sentence about Developer Mode.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Skipf("symlink fixture needs SeCreateSymbolicLinkPrivilege, which this host does not grant "+
			"(Developer Mode off and process not elevated); the behaviour under test is NOT verified here: %v", err)
	}
	require.NoError(t, err)
}
