//go:build windows

package main

// ordinaryPathIsShellSafe says whether a plain project path survives a shell
// unrendered on this host.
//
// Every absolute Windows path carries backslashes, which a shell eats, so the
// SCOPE column is rendered even for an ordinary path. That is the renderer
// working, not a regression in the table.
const ordinaryPathIsShellSafe = false
