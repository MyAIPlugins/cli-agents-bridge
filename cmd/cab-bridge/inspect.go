package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// runInspect prints a session manifest as JSON on stdout. Replaces the jq
// runtime dependency from Patil's bash bridge — any caller wanting to grep
// a specific field can pipe `cab-bridge inspect <id> --json | jq .role`.
func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", true, "emit JSON on stdout (default true)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
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
		out, err := json.MarshalIndent(mf, "", "  ")
		if err != nil {
			return fmt.Errorf("inspect: marshal: %w", err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Printf("Session %s (%s, %s)\n", mf.SessionID, mf.Role, mf.AgentName)
		fmt.Printf("  Project: %s (%s)\n", mf.ProjectName, mf.ProjectPath)
		fmt.Printf("  PID:     %d\n", mf.PID)
		fmt.Printf("  Started: %s\n", mf.StartedAt.Format("2006-01-02 15:04:05 MST"))
		fmt.Printf("  HB age:  %s\n", mf.LastHeartbeat.Format("2006-01-02 15:04:05 MST"))
	}
	return nil
}
