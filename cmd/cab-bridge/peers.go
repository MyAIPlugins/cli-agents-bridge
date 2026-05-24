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
}

func runPeers(args []string) error {
	fs := flag.NewFlagSet("peers", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON array on stdout (default: human tabwriter)")
	includeStale := fs.Bool("include-stale", true, "include sessions whose lastHeartbeat exceeds StaleSeconds")
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

	peers, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, *includeStale)
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
	fmt.Fprintln(tw, "SESSION_ID\tROLE\tAGENT_NAME\tPROJECT\tPID\tHEARTBEAT_AGE\tSTALE")
	now := time.Now().UTC()
	for _, p := range peers {
		age := now.Sub(p.LastHeartbeat).Truncate(time.Second)
		stale := "ok"
		if p.Stale {
			stale = "STALE"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			p.SessionID, p.Role, p.AgentName, p.ProjectName, p.PID, age, stale)
	}
	return tw.Flush()
}

func collectPeers(mgr *session.Manager, dataDir string, staleSeconds int, includeStale bool) ([]peerSummary, error) {
	sessionsRoot := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("peers: read sessions root: %w", err)
	}

	cutoff := time.Now().UTC().Add(-time.Duration(staleSeconds) * time.Second)
	var out []peerSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := mgr.LoadManifest(e.Name())
		if err != nil {
			continue
		}
		stale := mf.LastHeartbeat.Before(cutoff)
		if stale && !includeStale {
			continue
		}
		out = append(out, peerSummary{
			SessionID:     mf.SessionID,
			Role:          mf.Role,
			AgentName:     mf.AgentName,
			ProjectName:   mf.ProjectName,
			PID:           mf.PID,
			LastHeartbeat: mf.LastHeartbeat,
			Stale:         stale,
		})
	}
	return out, nil
}
