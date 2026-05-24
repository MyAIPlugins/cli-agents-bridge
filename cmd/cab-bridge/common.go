package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// loadConfigOrFail loads runtime config and prints warnings to stderr.
// Returns the resolved config; on load failure returns a wrapped error
// the caller surfaces with exit 1.
func loadConfigOrFail() (config.Config, error) {
	cfg, warnings, err := config.Load()
	if err != nil {
		return cfg, fmt.Errorf("load config: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "config warning:", w)
	}
	return cfg, nil
}

// newSessionManager builds a session.Manager from a loaded config. All
// subcommands that touch sessions share this constructor for consistency.
func newSessionManager(cfg config.Config) *session.Manager {
	return session.NewManager(cfg.DataDir, time.Duration(cfg.HeartbeatTickMs)*time.Millisecond)
}

// resolveSessionID returns the session ID to operate on. If flagValue is
// non-empty it is validated via SC-4 and returned. Otherwise the function
// looks up the longest-prefix-match for the current working directory and
// returns that ID. Returns a wrapped error suitable for stderr+exit on
// any failure.
func resolveSessionID(mgr *session.Manager, flagValue string) (string, error) {
	if flagValue != "" {
		if err := security.ValidateSessionID(flagValue); err != nil {
			return "", err
		}
		return flagValue, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd for session lookup: %w", err)
	}
	sid, err := mgr.LongestPrefixLookup(cwd)
	if err != nil {
		if errors.Is(err, session.ErrNoSessionForCwd) {
			return "", fmt.Errorf("no session found for cwd %q — register first with `cab-bridge register` or pass --session-id", cwd)
		}
		return "", fmt.Errorf("session lookup from cwd %q: %w", cwd, err)
	}
	return sid, nil
}
