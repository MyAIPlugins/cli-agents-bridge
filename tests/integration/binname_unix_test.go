//go:build !windows

package integration

// binExeSuffix is appended to the harness binary built by buildBinary. On Unix
// an extensionless name is executable as-is.
const binExeSuffix = ""
