package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// runState sets the calling session's agent task-state (F-23a). Usage:
//
//	cab-bridge state [--session-id=<id>] <value>
//
// Flags must precede the positional <value> (Go's flag package stops at the
// first non-flag arg). The common form `cab-bridge state working` resolves the
// session from the cwd. Reading the state is via whoami/status/peers — this
// command is set-only.
func runState(args []string) error {
	fs_ := flag.NewFlagSet("state", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	sessionIDFlag := fs_.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	if err := fs_.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	rest := fs_.Args()
	if len(rest) != 1 {
		return fmt.Errorf("state: exactly one state value required (one of: %s) — usage: cab-bridge state [--session-id=<id>] <value>", session.StatesHint())
	}
	value := rest[0]
	if !session.IsValidState(value) {
		return fmt.Errorf("state: invalid value %q — must be one of: %s", value, session.StatesHint())
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	sid, err := resolveSessionID(mgr, *sessionIDFlag)
	if err != nil {
		return err
	}
	if err := mgr.SetState(sid, value); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	fmt.Printf("state: %s -> %s\n", sid, value)
	return nil
}
