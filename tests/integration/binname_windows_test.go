//go:build windows

package integration

// binExeSuffix is appended to the harness binary built by buildBinary. Windows
// refuses to exec a file whose name carries no PATHEXT extension, so every
// end-to-end scenario dies at exec time without it.
const binExeSuffix = ".exe"
