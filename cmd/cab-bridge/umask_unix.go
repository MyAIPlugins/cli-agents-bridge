//go:build !windows

package main

import "syscall"

// SC-1, and it has to run before anything creates a file or a directory — which
// is why it is an init() and not a line in main(): several packages build paths
// during their own initialisation.
//
// umask 0o077 makes every file cab-bridge creates 0o600 and every directory
// 0o700 by default, protecting session manifests, inbox/outbox messages and lock
// files from other-uid readers (threat model TM-1). The explicit modes passed to
// MkdirAll/WriteFile are the belt; this is the braces.
func init() {
	syscall.Umask(0o077)
}
