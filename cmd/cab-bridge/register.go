package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	role := fs.String("role", session.RoleNeutral, "session role — "+session.RoleNamesWithNote()+"; neutral is the v1-read fallback")
	agentName := fs.String("agent-name", "", "human-readable name (default: project basename)")
	projectPath := fs.String("project-path", "", "project root path (default: cwd)")
	forceNew := fs.Bool("force-new", false, "override existing live session for the same project (BUG-6)")
	resume := fs.Bool("resume", false, "resume an existing matching session (same agent-name/role/scope/team) instead of creating a new one — the idempotent post-compact/restart bootstrap (F-27); errors if a live session with this identity already exists (use --force-new for a second instance)")
	team := fs.String("team", "", "team label isolating this pair from others in the same data dir (F-5); peers --team filters on it")
	asJSON := fs.Bool("json", true, "emit registration manifest as JSON on stdout (default true)")
	// A-5: register has no --session-id (the id is DERIVED, not supplied). Define
	// it only to reject it with an actionable message — the "always pass
	// --session-id" rule (correct for the shared-scope collision) otherwise hits
	// the cryptic stdlib "flag provided but not defined: -session-id" here.
	sessionIDFlag := fs.String("session-id", "", "not supported here — register derives the id from (agent-name, role, scope); use --resume to reconnect an existing session")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *sessionIDFlag != "" {
		return errors.New("register: --session-id is not supported here — register derives the id from (agent-name, role, scope); to resume an existing session use `cab-bridge register --resume`")
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

	pp := *projectPath
	if pp == "" {
		var werr error
		pp, werr = os.Getwd()
		if werr != nil {
			return fmt.Errorf("register: getwd: %w", werr)
		}
	}

	// Absolute before anything reads it, for the reason spelled out in join.go:
	// Register stores filepath.Abs of this, and every lookup compares against
	// what is stored.
	if abs, aerr := filepath.Abs(pp); aerr == nil {
		pp = abs
	}

	scope := resolveScope(pp)

	// SAY IT WHEN THE NAME IS NOT THE DIRECTORY'S (F-124 P2-5).
	//
	// SanitizeDerivedName returns `changed` precisely so the caller can say so
	// out loud, and `join` does. `register` did not: it printed the id and
	// nothing else, so a project called `my repo` silently became `my_20repo`
	// with no line anywhere. Same family as the silent rename this whole lot
	// exists to remove — the difference between the two commands was never a
	// decision, just the one that got written first.
	//
	// stderr, so stdout stays the manifest JSON or the bare id that scripts read.
	if *agentName == "" {
		if derived, changed := session.SanitizeDerivedName(pp); changed {
			fmt.Fprintf(os.Stderr, "register: this directory is called %q, which cannot be used as an agent name as it stands — deriving from %q instead\n",
				filepath.Base(pp), derived)
		}
	}

	// Auto-gc orphan sessions before creating a new one (v0.2.1, F10). Sweeps
	// sessions whose owning PID is dead AND heartbeat is older than AutoGCHours
	// (no daemon — the sweep piggybacks on a command the user already runs), and
	// only within THIS caller's scope: an arrival is not a mandate to collect
	// another team's abandoned work. Logged on stderr so the manifest JSON on
	// stdout stays clean.
	runAutoGC(cfg, scope, os.Stderr)

	mf, release, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: pp,
		AgentName:   *agentName,
		Role:        *role,
		ForceNew:    *forceNew,
		TeamID:      *team,
		Scope:       scope,   // F-17: auto project-root; "" on non-fatal failure
		Resume:      *resume, // F-27: reconnect-or-register
	})
	if err != nil {
		return err
	}
	// register subcommand only writes the manifest; lock release is the
	// caller's responsibility for short-lived "register and exit" runs.
	// We release immediately so a subsequent `listen` from a different
	// process can re-acquire. listen will re-acquire its own lock.
	_ = release()

	// B-2: a --resume that reclaimed a live identity match reports what it
	// superseded on stderr (stdout stays the manifest JSON / bare id for capture).
	// The previous listener is already revoked; its orphan listen, at its next
	// ownership check, stops consuming and exits.
	if mf.LastReclaim != nil {
		fmt.Fprintf(os.Stderr, "register: reclaimed session %s — superseded the previous listener (pid %d, generation %d -> %d); a prior instance with this identity was orphaned\n",
			mf.SessionID, mf.LastReclaim.PrevPID, mf.LastReclaim.PrevGeneration, mf.LastReclaim.NewGeneration)
	}

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
