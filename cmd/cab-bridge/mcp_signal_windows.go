//go:build windows

package main

import (
	"errors"
	"os"
)

// There is no SIGPIPE here, and the MCP server does not start on Windows anyway
// (no identity lock): this only has to compile.
func swallowSIGPIPE() (stop func()) { return func() {} }

var mcpStopSignals = []os.Signal{os.Interrupt}

// mcpPlatformCheck refuses to start: the port has no identity lock on Windows and a
// lock that is not exclusive would be worse than none (F-72 is out of scope there).
func mcpPlatformCheck() error {
	return errors.New("cab-bridge mcp: not starting — the MCP connector is not available on Windows (it needs an exclusive identity lock that this platform does not provide here)")
}
