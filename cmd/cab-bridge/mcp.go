package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

// mcp is the connector Claude Desktop runs (F-72): an MCP server on stdio that lets a
// Desktop conversation, in chat or in Cowork, work with the val of a project whose
// bridge that val has opened (`cab-bridge desktop open`).
//
// Desktop starts one of these per surface when the app launches and talks to it over
// its standard input and output. That is also why the start-up is strict: the process
// begins with a working directory of `/` and five environment variables, the protocol
// owns stdout, and a failure that is not loud leaves Desktop believing it is connected
// to a bridge that does not exist.
const mcpUsage = "usage: cab-bridge mcp   (started by Claude Desktop; it takes no arguments and speaks MCP on stdin and stdout)"

func runMCP(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("mcp: takes no arguments\n%s", mcpUsage)
	}
	// The guards come first, before anything that could create a directory: loading the
	// configuration already does.
	if err := mcpPlatformCheck(); err != nil {
		return err
	}
	if err := mcpPreflight(os.UserHomeDir, os.Getenv); err != nil {
		return err
	}
	protocolOut := reserveProtocolStdout()
	stopPipe := swallowSIGPIPE()
	defer stopPipe()

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	if err := validateMCPConfig(cfg); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cab-bridge mcp: not starting — no home directory: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cab-bridge mcp: not starting — cannot tell where this binary is: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), mcpStopSignals...)
	defer stop()
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, "cab-bridge: "+format+"\n", args...) }
	app := newMCPApp(ctx, cfg, newSessionManager(cfg), exe, home, logf)
	defer app.close()
	srv, err := app.newServer()
	if err != nil {
		return fmt.Errorf("cab-bridge mcp: %w", err)
	}
	logf("mcp: serving, data dir %s", cfg.DataDir)
	if err := srv.Serve(ctx, os.Stdin, protocolOut); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cab-bridge mcp: %w", err)
	}
	return nil
}
