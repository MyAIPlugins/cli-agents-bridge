//go:build windows

package main

import (
	"io/fs"
	"testing"
)

// ownedByAnotherUser is false on Windows: ownership there is a SID comparison done
// in internal/security, and the foreign-owner case is skipped by name above it.
func ownedByAnotherUser(t *testing.T, _ fs.FileInfo) bool {
	t.Helper()
	return false
}
