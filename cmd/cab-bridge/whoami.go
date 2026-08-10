package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
)

// whoamiReport is the identity of the current session: who am I (session,
// agent, role, team) and WHERE am I (projectPath full path + dataDir). The
// dataDir field is the direct F-5 diagnostic — the recurring failure was a
// session registered in the wrong data dir (a forgotten CAB_DATA_DIR); whoami
// makes that immediately visible. ProjectPath is the FULL path (not the
// basename projectName shown by peers), which disambiguates two projects whose
// cwd basename collides (F-6).
type whoamiReport struct {
	SessionID   string `json:"sessionId"`
	AgentName   string `json:"agentName"`
	Role        string `json:"role"`
	TeamID      string `json:"teamId,omitempty"`
	ProjectPath string `json:"projectPath"`
	// Scope is the project root this session belongs to — the EFFECTIVE one, so
	// it is the same answer the routing acts on. It used to be the stored field
	// verbatim, which meant a legacy session read "(none)" here while being
	// addressed as part of /repo/a: the tool gave two answers and the agent saw
	// the wrong one.
	Scope string `json:"scope,omitempty"`
	// ScopeDerived says the value above was worked out from the project path
	// rather than read from the manifest. A derived scope is a fact about the
	// filesystem NOW, and an agent is entitled to know which of the two it has.
	ScopeDerived bool `json:"scopeDerived,omitempty"`
	// State is the F-23a agent task-state (idle/working/done/orchestrating) from
	// the stored manifest; empty (omitted) for legacy/never-set.
	State   string `json:"state,omitempty"`
	DataDir string `json:"dataDir"`
}

func runWhoami(args []string) error {
	fs_ := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	sessionIDFlag := fs_.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	asJSON := fs_.Bool("json", false, "emit JSON on stdout (default: human-readable)")
	if err := fs_.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	sid, err := resolveCurrentSession(mgr, "whoami", *sessionIDFlag)
	if err != nil {
		return err
	}
	mf, err := mgr.LoadManifest(sid)
	if err != nil {
		return err
	}

	report := whoamiReport{
		SessionID:    mf.SessionID,
		AgentName:    mf.AgentName,
		Role:         mf.Role,
		TeamID:       mf.TeamID,
		ProjectPath:  mf.ProjectPath,
		Scope:        effectiveScope(mf),
		ScopeDerived: scopeIsDerived(mf),
		State:        mf.State,
		DataDir:      cfg.DataDir,
	}

	if *asJSON {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("whoami: marshal: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	team := report.TeamID
	if team == "" {
		team = "(none)"
	}
	// The EFFECTIVE scope (set above), plus a word when it had to be worked out.
	// A legacy session used to read "(none)" here while the system routed it as
	// belonging to /repo/a: two answers to the same question, and the one the
	// agent reads was the wrong one.
	scope := report.Scope
	if scope == "" {
		scope = "(none)"
	} else if report.ScopeDerived {
		scope += "  (derived from the project path)"
	}
	state := report.State
	if state == "" {
		state = "(none)"
	}
	fmt.Printf("session:     %s\n", report.SessionID)
	fmt.Printf("agent:       %s\n", report.AgentName)
	fmt.Printf("role:        %s\n", report.Role)
	fmt.Printf("team:        %s\n", team)
	fmt.Printf("projectPath: %s\n", report.ProjectPath)
	fmt.Printf("scope:       %s\n", scope)
	fmt.Printf("state:       %s\n", state)
	fmt.Printf("dataDir:     %s\n", report.DataDir)
	return nil
}
