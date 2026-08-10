package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// runInspect prints a session manifest as JSON on stdout. Replaces the jq
// runtime dependency from Patil's bash bridge — any caller wanting to grep
// a specific field can pipe `cab-bridge inspect <id> --json | jq .role`.
func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", true, "emit JSON on stdout (default true)")
	// A-5: inspect takes the id as a POSITIONAL argument, not a flag. Define
	// --session-id only to reject it with an actionable message instead of the
	// cryptic stdlib "flag provided but not defined: -session-id".
	sessionIDFlag := fs.String("session-id", "", "not supported here — pass the session id as a positional argument: `cab-bridge inspect <id>`")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *sessionIDFlag != "" {
		return errors.New("inspect: --session-id is not supported here — pass the session id as a positional argument: `cab-bridge inspect <id>`")
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("inspect: expected exactly one positional argument <session-id>")
	}
	sid := rest[0]
	if err := security.ValidateSessionID(sid); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)
	mf, err := mgr.LoadManifest(sid)
	if err != nil {
		return err
	}

	if *asJSON {
		// SECOND declared forensic exception, and the one no grep can find: this
		// serialises the manifest by REFLECTION, so `scope` goes out raw without
		// any line of code mentioning the field. Right for a forensic dump — the
		// record as it is on disk — but it has to be named here, or whoever
		// applies the criterion finds it and cannot tell deliberate from missed.
		out, err := json.MarshalIndent(mf, "", "  ")
		if err != nil {
			return fmt.Errorf("inspect: marshal: %w", err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Printf("Session %s (%s, %s)\n", mf.SessionID, mf.Role, mf.AgentName)
		fmt.Printf("  Project: %s (%s)\n", mf.ProjectName, mf.ProjectPath)
		// The FULL scope path, which `peers` no longer prints: that column now
		// shows the basename, because the basename is what identifies the group
		// and what an agent reads. The whole path stays here, in --json, and in
		// the self views (`whoami`, `overview`) — none of which is read in a
		// hurry.
		//
		// The list matters, and the first draft of this comment got it wrong by
		// saying "here and in --json": somebody trusting it would have concluded
		// that dropping the path from `overview` costs nothing. Caught by the val
		// on the day we counted thirteen of these.
		// BOTH, because this is the forensic view: the effective scope is the
		// answer the system acts on, the raw field is a fact about the record.
		// Everywhere else shows only the first — two answers to "which project am
		// I in" is what left a legacy session reading "(none)" while being routed
		// as if it were in /repo/a.
		if eff := session.EffectiveScope(mf); eff != "" {
			if session.ScopeIsDerived(mf) {
				fmt.Printf("  Scope:   %s (derived from the project path; not in the manifest)\n", eff)
			} else {
				fmt.Printf("  Scope:   %s\n", eff)
			}
		}
		fmt.Printf("  PID:     %d\n", mf.PID)
		fmt.Printf("  Started: %s\n", mf.StartedAt.Format("2006-01-02 15:04:05 MST"))
		fmt.Printf("  HB age:  %s\n", mf.LastHeartbeat.Format("2006-01-02 15:04:05 MST"))
		// B-2: the listener ownership record, when present (generation + pid +
		// claimed-at). pid==0 means a reclaim revoked it and no listener re-claimed.
		if owner, ok, oerr := mgr.ReadListener(sid); oerr == nil && ok {
			fmt.Printf("  Listener: generation %d, pid %d, claimed %s\n",
				owner.Generation, owner.PID, owner.ClaimedAt.Format("2006-01-02 15:04:05 MST"))
			if owner.PID == 0 {
				fmt.Printf("            reclaim-pending (revoked, no listener has re-claimed)\n")
			}
		}
	}
	return nil
}
