//go:build windows

package main

// There is no SIGPIPE here, and the MCP server does not start on Windows anyway
// (no identity lock): this only has to compile.
func swallowSIGPIPE() (stop func()) { return func() {} }
