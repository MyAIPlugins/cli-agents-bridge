//go:build !windows

package main

// ordinaryPathIsShellSafe says whether a plain project path survives a shell
// unrendered on this host.
//
// An absolute POSIX path carries no byte a shell would touch, so the SCOPE
// column prints it bare and the table reads as it always did.
const ordinaryPathIsShellSafe = true
