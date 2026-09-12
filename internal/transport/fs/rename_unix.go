//go:build !windows

package fs

import "os"

// renameAtomic is os.Rename, and on this platform it is nothing else.
//
// The Windows half of this pair is thirty lines of Win32 because the obvious
// call cannot replace a file somebody is reading (F-133). None of that belongs
// here: rename(2) has always had the semantics the rest of this package assumes,
// so the honest port of it is the call itself.
//
// Deliberately NOT symmetrical with rename_windows.go — no retry, no wrapping,
// no shared helper. A file that looks like its counterpart but is not would
// invite somebody to "unify" the two and give Unix a retry loop for a failure
// mode it does not have.
func renameAtomic(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
