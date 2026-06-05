package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

type statusReport struct {
	SessionID      string    `json:"sessionId"`
	Role           string    `json:"role"`
	AgentName      string    `json:"agentName"`
	ProjectName    string    `json:"projectName"`
	StartedAt      time.Time `json:"startedAt"`
	LastHeartbeat  time.Time `json:"lastHeartbeat"`
	HeartbeatAge   string    `json:"heartbeatAge"`
	InboxCount     int       `json:"inboxCount"`
	OutboxCount    int       `json:"outboxCount"`
	ProcessedCount int       `json:"processedCount"`
	Stale          bool      `json:"stale"`
	// State is the F-23a agent task-state (idle/working/done/orchestrating);
	// empty (omitted) for legacy/never-set. orchestrating forces Stale=false
	// (session.IsStale).
	State string `json:"state,omitempty"`
	// LastConsumedMsgID is the id of the most recently consumed inbox message
	// (F-12). Combined with inboxCount (== pending, since consumed messages are
	// moved to processed/) it distinguishes an idle session from one actively
	// draining its inbox. Empty (omitted) until the session consumes its first
	// message - see the manifest field doc for the VAL-orchestrator case.
	LastConsumedMsgID string `json:"lastConsumedMsgId,omitempty"`
}

func runStatus(args []string) error {
	fs_ := flag.NewFlagSet("status", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	sessionIDFlag := fs_.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
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

	sid, err := resolveCurrentSession(mgr, "status", *sessionIDFlag)
	if err != nil {
		return err
	}
	mf, err := mgr.LoadManifest(sid)
	if err != nil {
		return err
	}

	sessionDir := filepath.Join(cfg.DataDir, "sessions", sid)
	report := statusReport{
		SessionID:         mf.SessionID,
		Role:              mf.Role,
		AgentName:         mf.AgentName,
		ProjectName:       mf.ProjectName,
		StartedAt:         mf.StartedAt,
		LastHeartbeat:     mf.LastHeartbeat,
		HeartbeatAge:      time.Since(mf.LastHeartbeat).Truncate(time.Second).String(),
		InboxCount:        countJSON(filepath.Join(sessionDir, "inbox")),
		OutboxCount:       countJSON(filepath.Join(sessionDir, "outbox")),
		ProcessedCount:    countJSON(filepath.Join(sessionDir, "processed")),
		Stale:             session.IsStale(mf, cfg.StaleSeconds, time.Now().UTC()),
		LastConsumedMsgID: mf.LastConsumedMsgID,
		State:             mf.State,
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("status: marshal: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

// countJSON returns the number of .json files in dir (non-recursive). Missing
// dir yields 0 (lazy creation of inbox/outbox/processed is expected).
func countJSON(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if len(e.Name()) >= 5 && e.Name()[len(e.Name())-5:] == ".json" {
			n++
		}
	}
	return n
}
