package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
)

// Claude Desktop's relay gives up on a tool call after 60 seconds: a 55 s call came
// back, a 65 s one did not, and the error message names 60 (measured, F-72 spike).
// A call's whole time, its wait included, has to stay well under that, with a margin
// for the relay's own latency, which was not measured.
const (
	relayCallBudgetSeconds = 60
	relaySafetyMarginSecs  = 10
)

// Start-up guards of `cab-bridge mcp` (F-72). They live apart from the server so
// they can be tested without one, and they run BEFORE anything else: before the
// configuration is loaded, because loading it already creates the data directory.

// mcpPreflight refuses to start without a stable environment (F-145).
//
// Claude Desktop starts the server with a working directory of `/` and five
// environment variables, one of which happens to be HOME. Without HOME the
// product does not fail: DefaultConfig ignores the error from os.UserHomeDir, the
// data directory becomes the RELATIVE path `.claude/cli-agents-bridge`, the loader
// resolves it against the working directory with a warning, the project-root guard
// of F-143 quietly turns itself off for lack of a home to compare with, and the
// process registers in a bridge of its own, says "registered-new, nobody else is
// here yet" and exits 0 (executed in a sandbox). For a CLI that is a nuisance; for
// a server that Desktop believes is connected to the bridge it is the worst
// outcome there is, a partition nobody can see. So there is no fallback: the
// server does not start, and says why.
//
// homeDir and getenv are injected so the cases can be tested without touching
// the process environment.
func mcpPreflight(homeDir func() (string, error), getenv func(string) string) error {
	home, err := homeDir()
	if err != nil {
		return fmt.Errorf("cab-bridge mcp: not starting — no home directory (%w). The bridge's data directory and its project-root guard both derive from $HOME; "+
			"started without it the process would build a private bridge in its working directory (F-145). Set HOME, or CAB_DATA_DIR to an absolute path and HOME as well", err)
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("cab-bridge mcp: not starting — the home directory %q is not an absolute path", home)
	}
	if dir := getenv("CAB_DATA_DIR"); dir != "" && !filepath.IsAbs(dir) {
		return fmt.Errorf("cab-bridge mcp: not starting — CAB_DATA_DIR=%q is not an absolute path; relative to the working directory it would name a different bridge for every launch", dir)
	}
	return nil
}

// reserveProtocolStdout hands the real standard output to the caller, which is the
// only thing allowed to write the protocol on it, and points os.Stdout at stderr.
//
// The server's stdout IS the protocol: one line that is not a JSON-RPC message
// breaks the framing for the client. The code reused by the tools prints to
// os.Stdout in many places (the join report, peers, inspect, whoami), so "remember
// not to" is not a defence. This makes the mistake impossible instead: after the
// call, a stray fmt.Println lands on stderr, which Desktop keeps in its log and
// reads as diagnostics, and the protocol writer is the only holder of the real one.
func reserveProtocolStdout() *os.File {
	real := os.Stdout
	os.Stdout = os.Stderr
	return real
}

// validateMCPConfig refuses limits that cannot be honoured. The wait of a `next` is
// the one that matters: a call that outlasts the relay's patience is a call whose
// answer nobody receives, and the server would not know. Every number must be
// positive (a zero would mean "no limit" to somebody), the default wait cannot exceed
// the maximum, and the wait plus the time allowed to read the inbox, like the child's
// deadline, must fit under the relay's budget minus the margin.
func validateMCPConfig(cfg config.Config) error {
	limit := relayCallBudgetSeconds - relaySafetyMarginSecs
	positive := []struct {
		name string
		v    int
	}{
		{"mcp_next_max_wait_seconds", cfg.MCPNextMaxWaitSeconds},
		{"mcp_next_default_wait_seconds", cfg.MCPNextDefaultWaitSeconds},
		{"mcp_scan_budget_seconds", cfg.MCPScanBudgetSeconds},
		{"mcp_child_timeout_seconds", cfg.MCPChildTimeoutSeconds},
		{"mcp_max_result_bytes", cfg.MCPMaxResultBytes},
		{"mcp_grant_ttl_minutes", cfg.MCPGrantTTLMinutes},
		{"poll_interval_ms", cfg.PollIntervalMs},
	}
	for _, p := range positive {
		if p.v <= 0 {
			return fmt.Errorf("cab-bridge mcp: not starting — %s is %d; it must be positive", p.name, p.v)
		}
	}
	if cfg.MCPNextDefaultWaitSeconds > cfg.MCPNextMaxWaitSeconds {
		return fmt.Errorf("cab-bridge mcp: not starting — mcp_next_default_wait_seconds (%d) is larger than mcp_next_max_wait_seconds (%d)",
			cfg.MCPNextDefaultWaitSeconds, cfg.MCPNextMaxWaitSeconds)
	}
	if total := cfg.MCPNextMaxWaitSeconds + cfg.MCPScanBudgetSeconds; total > limit {
		return fmt.Errorf("cab-bridge mcp: not starting — a next call could take %d s (mcp_next_max_wait_seconds %d + mcp_scan_budget_seconds %d), "+
			"but Claude Desktop gives up on a tool call after %d s and the answer would be lost; keep it at %d s or less",
			total, cfg.MCPNextMaxWaitSeconds, cfg.MCPScanBudgetSeconds, relayCallBudgetSeconds, limit)
	}
	if cfg.MCPChildTimeoutSeconds > limit {
		return fmt.Errorf("cab-bridge mcp: not starting — mcp_child_timeout_seconds (%d) is above %d s, the most a call may take under Claude Desktop's %d s limit",
			cfg.MCPChildTimeoutSeconds, limit, relayCallBudgetSeconds)
	}
	if cfg.MCPMaxResultBytes < mcp.MinResultBytes {
		return fmt.Errorf("cab-bridge mcp: not starting — mcp_max_result_bytes (%d) is below the floor of %d bytes", cfg.MCPMaxResultBytes, mcp.MinResultBytes)
	}
	return nil
}
