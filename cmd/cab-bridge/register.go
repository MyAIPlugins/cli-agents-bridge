package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	role := fs.String("role", session.RoleNeutral, "session role (val|esc|architect|observer|neutral)")
	agentName := fs.String("agent-name", "", "human-readable name (default: project basename)")
	projectPath := fs.String("project-path", "", "project root path (default: cwd)")
	forceNew := fs.Bool("force-new", false, "override existing live session for the same project (BUG-6)")
	team := fs.String("team", "", "team label isolating this pair from others in the same data dir (F-5); peers --team filters on it")
	asJSON := fs.Bool("json", true, "emit registration manifest as JSON on stdout (default true)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// Validate the team label only when provided — empty is valid ("no team").
	if *team != "" {
		if err := security.ValidateTeamID(*team); err != nil {
			return err
		}
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	// Auto-gc orphan sessions before creating a new one (v0.2.1, F10). Sweeps
	// sessions whose owning PID is dead AND heartbeat is older than AutoGCHours
	// (no daemon — the sweep piggybacks on a command the user already runs).
	// Logged on stderr so the manifest JSON on stdout stays clean.
	runAutoGC(cfg, os.Stderr)

	pp := *projectPath
	if pp == "" {
		var werr error
		pp, werr = os.Getwd()
		if werr != nil {
			return fmt.Errorf("register: getwd: %w", werr)
		}
	}

	mf, release, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: pp,
		AgentName:   *agentName,
		Role:        *role,
		ForceNew:    *forceNew,
		TeamID:      *team,
		Scope:       resolveScope(pp), // F-17: auto project-root; "" on non-fatal failure
	})
	if err != nil {
		return err
	}
	// register subcommand only writes the manifest; lock release is the
	// caller's responsibility for short-lived "register and exit" runs.
	// We release immediately so a subsequent `listen` from a different
	// process can re-acquire. listen will re-acquire its own lock.
	_ = release()

	if *asJSON {
		out, err := json.MarshalIndent(mf, "", "  ")
		if err != nil {
			return fmt.Errorf("register: marshal: %w", err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Println(mf.SessionID)
	}
	return nil
}
