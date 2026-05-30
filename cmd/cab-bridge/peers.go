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
	// Scope is the F-17 auto-derived project-root path. peers filters on it by
	// default (the cwd's scope); empty (omitted) for legacy/pre-F-17 sessions,
	// which are therefore hidden by the default filter and shown only with
	// --all-scopes.
	Scope string `json:"scope,omitempty"`
}

func runPeers(args []string) error {
	fs := flag.NewFlagSet("peers", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON array on stdout (default: human tabwriter)")
	includeStale := fs.Bool("include-stale", true, "include sessions whose lastHeartbeat exceeds StaleSeconds")
	team := fs.String("team", "", "show only sessions whose teamId matches (F-5 isolation); default: all sessions")
	allScopes := fs.Bool("all-scopes", false, "show every scope (disable the F-17 default filter to the cwd's project root)")
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

	// F-17 default isolation: filter to the cwd's project-root scope so a fresh
	// session sees only its own project's pair. --all-scopes and --team are
	// explicit cross-scope views, so either one disables the scope filter (H3):
	// --team keeps its existing global-by-teamId semantics, --all-scopes shows
	// everything. Scope detection failure is non-fatal (resolveScope logs and
	// returns "" -> show all).
	scopeFilter := ""
	if !*allScopes && *team == "" {
		cwd, werr := os.Getwd()
		if werr != nil {
			fmt.Fprintf(os.Stderr, "cab-bridge: cannot resolve cwd for scope filter (non-fatal): %v — showing all scopes\n", werr)
		} else {
			scopeFilter = resolveScope(cwd)
		}
	}

	peers, hiddenByScope, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, *includeStale, *team, scopeFilter)
	if err != nil {
		return err
	}

	// Anti-silent-cap (project no-silent-truncation discipline): when the default
	// scope filter hid sessions, say so on stderr so the user never mistakes a
	// filtered list for the whole picture. stdout (table/JSON) stays clean for
	// scripts.
	if hiddenByScope > 0 {
		fmt.Fprintf(os.Stderr, "cab-bridge: %d session(s) in other scopes hidden — use --all-scopes to show\n", hiddenByScope)
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
	fmt.Fprintln(tw, "SESSION_ID\tROLE\tAGENT_NAME\tPROJECT\tTEAM\tPID\tHEARTBEAT_AGE\tSTALE\tINBOX\tLAST_CONSUMED\tSCOPE")
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
		scopeCol := p.Scope
		if scopeCol == "" {
			scopeCol = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%d\t%s\t%s\n",
			p.SessionID, p.Role, p.AgentName, p.ProjectName, teamCol, p.PID, age, stale, p.InboxCount, lastConsumed, scopeCol)
	}
	return tw.Flush()
}

// collectPeers lists peer sessions. teamFilter, when non-empty, restricts the
// result to sessions whose teamId matches exactly (F-5); sessions without a team
// are therefore excluded by it. scopeFilter, when non-empty, restricts the
// result to sessions whose scope matches exactly (F-17); legacy/pre-F-17
// sessions have an empty scope and are excluded by it. Either empty filter is a
// no-op (the unchanged global default for that axis).
//
// The second return value is the number of sessions that passed the team and
// stale checks but were excluded SOLELY by scopeFilter — i.e. how many more the
// caller would see with --all-scopes. The caller uses it for the anti-silent-cap
// stderr hint. It is always 0 when scopeFilter is empty.
func collectPeers(mgr *session.Manager, dataDir string, staleSeconds int, includeStale bool, teamFilter, scopeFilter string) ([]peerSummary, int, error) {
	sessionsRoot := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []peerSummary{}, 0, nil // BUG-B: empty, not nil, for JSON []
		}
		return nil, 0, fmt.Errorf("peers: read sessions root: %w", err)
	}

	cutoff := time.Now().UTC().Add(-time.Duration(staleSeconds) * time.Second)
	out := []peerSummary{} // BUG-B: empty, not nil, so peers --json emits [] not null
	hiddenByScope := 0
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
		// Scope filter last, after team+stale, so hiddenByScope counts exactly
		// the sessions --all-scopes would reveal under the same other flags.
		if scopeFilter != "" && mf.Scope != scopeFilter {
			hiddenByScope++
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
			Scope:             mf.Scope,
		})
	}
	return out, hiddenByScope, nil
}
