//go:build !windows

package main

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
)

// ownedByAnotherUser reports whether the directory belongs to a user other than the
// one running the test. The uid comes from the same Stat_t the binary reads.
func ownedByAnotherUser(t *testing.T, info fs.FileInfo) bool {
	t.Helper()
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("no Stat_t behind %T: cannot read the owner", info.Sys())
	}
	return int(st.Uid) != os.Getuid()
}
