package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

type peerSummary struct {
	SessionID     string    `json:"sessionId"`
	Role          string    `json:"role"`
	AgentName     string    `json:"agentName"`
	ProjectName   string    `json:"projectName"`
	PID           int       `json:"pid"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	Stale         bool      `json:"stale"`
	// InboxCount is the number of un-consumed messages in the peer's inbox
	// (consumed ones are already moved to processed/), so it doubles as the
	// pending count. LastConsumedMsgID is the id of the most recently consumed
	// message. Together they let an orchestrator tell an idle peer from one
	// actively draining its inbox (F-12), without relying on heartbeat - which
	// only proves the listen process is alive, not that work is happening.
	InboxCount        int    `json:"inboxCount"`
	LastConsumedMsgID string `json:"lastConsumedMsgId,omitempty"`
	// TeamID is the F-5 isolation label. Empty (omitted) for sessions
	// registered without --team. peers --team=<x> filters on it.
	TeamID string `json:"teamId,omitempty"`
}

func runPeers(args []string) error {
	fs := flag.NewFlagSet("peers", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON array on stdout (default: human tabwriter)")
	includeStale := fs.Bool("include-stale", true, "include sessions whose lastHeartbeat exceeds StaleSeconds")
	team := fs.String("team", "", "show only sessions whose teamId matches (F-5 isolation); default: all sessions")
	if err := fs.Parse(args); err != nil {
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

	peers, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, *includeStale, *team)
	if err != nil {
		return err
	}

	if *asJSON {
		out, err := json.MarshalIndent(peers, "", "  ")
		if err != nil {
			return fmt.Errorf("peers: marshal: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION_ID\tROLE\tAGENT_NAME\tPROJECT\tTEAM\tPID\tHEARTBEAT_AGE\tSTALE\tINBOX\tLAST_CONSUMED")
	now := time.Now().UTC()
	for _, p := range peers {
		age := now.Sub(p.LastHeartbeat).Truncate(time.Second)
		stale := "ok"
		if p.Stale {
			stale = "STALE"
		}
		lastConsumed := p.LastConsumedMsgID
		if lastConsumed == "" {
			lastConsumed = "-"
		}
		teamCol := p.TeamID
		if teamCol == "" {
			teamCol = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%d\t%s\n",
			p.SessionID, p.Role, p.AgentName, p.ProjectName, teamCol, p.PID, age, stale, p.InboxCount, lastConsumed)
	}
	return tw.Flush()
}

// collectPeers lists peer sessions. teamFilter, when non-empty, restricts the
// result to sessions whose teamId matches exactly (F-5); sessions without a team
// are therefore excluded by any filter. An empty teamFilter returns all sessions
// (the unchanged global default).
func collectPeers(mgr *session.Manager, dataDir string, staleSeconds int, includeStale bool, teamFilter string) ([]peerSummary, error) {
	sessionsRoot := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []peerSummary{}, nil // BUG-B: empty, not nil, for JSON []
		}
		return nil, fmt.Errorf("peers: read sessions root: %w", err)
	}

	cutoff := time.Now().UTC().Add(-time.Duration(staleSeconds) * time.Second)
	out := []peerSummary{} // BUG-B: empty, not nil, so peers --json emits [] not null
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := mgr.LoadManifest(e.Name())
		if err != nil {
			continue
		}
		if teamFilter != "" && mf.TeamID != teamFilter {
			continue
		}
		stale := mf.LastHeartbeat.Before(cutoff)
		if stale && !includeStale {
			continue
		}
		out = append(out, peerSummary{
			SessionID:         mf.SessionID,
			Role:              mf.Role,
			AgentName:         mf.AgentName,
			ProjectName:       mf.ProjectName,
			PID:               mf.PID,
			LastHeartbeat:     mf.LastHeartbeat,
			Stale:             stale,
			InboxCount:        countJSON(filepath.Join(sessionsRoot, e.Name(), "inbox")),
			LastConsumedMsgID: mf.LastConsumedMsgID,
			TeamID:            mf.TeamID,
		})
	}
	return out, nil
}
