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

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

type statusReport struct {
	SessionID     string    `json:"sessionId"`
	Role          string    `json:"role"`
	AgentName     string    `json:"agentName"`
	ProjectName   string    `json:"projectName"`
	StartedAt     time.Time `json:"startedAt"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	HeartbeatAge  string    `json:"heartbeatAge"`
	// InboxCount is the UNREAD count, not the number of files in inbox/: under the
	// mailbox model a delivered message stays there until `reply` archives it, so
	// the file count answers "how much mail is lying around" rather than "is there
	// anything to read". processedCount and outboxCount are still plain file
	// counts, which is what those two mean.
	InboxCount     int  `json:"inboxCount"`
	OutboxCount    int  `json:"outboxCount"`
	ProcessedCount int  `json:"processedCount"`
	Stale          bool `json:"stale"`
	// State is the F-23a agent task-state (idle/working/done/orchestrating);
	// empty (omitted) for legacy/never-set. orchestrating forces Stale=false
	// (session.IsStale).
	State string `json:"state,omitempty"`
	// LastConsumedMsgID is the id of the most recently consumed inbox message
	// (F-12). Read with inboxCount above it distinguishes an idle session from one
	// actively draining its inbox. Empty (omitted) until the session consumes its
	// first message - see the manifest field doc for the VAL-orchestrator case.
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
		InboxCount:        countUnread(mgr, sid, sessionDir, cfg.MaxMessageBytes),
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

// countUnread reports how many messages in a session's inbox a `next` would
// still deliver: those not yet NOTIFIED in the wake cursor, ack files excluded —
// the same two filters the delivery path applies (readMailbox + the cursor).
//
// It replaces the plain file count wherever the question is "does this agent
// have work waiting?". Under the mailbox model a delivered message STAYS in
// inbox/ — only `reply` archives — so from the day `next` became pure-read the
// file count answered a different question than the one being asked, and
// answered it in the direction that invents work: two messages both already
// read, both already acted on, reported as two waiting.
//
// An unreadable cursor counts everything as unread. The model is at-least-once,
// and overstating waiting work costs a look while understating it hides mail.
func countUnread(mgr *session.Manager, sid, sessionDir string, maxContentBytes int) int {
	entries, _, foreign, err := readMailbox(filepath.Join(sessionDir, "inbox"), maxContentBytes)
	// NEVER a silent zero: returning 0 here turned "there is a file I cannot
	// vouch for" into "your inbox is empty", which is the shape of the defect
	// this whole control exists to remove.
	for _, name := range foreign {
		fmt.Fprintf(os.Stderr, "cab-bridge: %s in this inbox does not belong to this user and was not counted\n", name)
	}
	if err != nil {
		return 0
	}
	cursor, _, err := mgr.ReadWakeCursor(sid)
	if err != nil {
		return len(entries)
	}
	n := 0
	for _, e := range entries {
		if !cursor.IsNotified(e.msg.ID) {
			n++
		}
	}
	return n
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
			// Same family as `sent`: a count derived from directory entries is
			// still a claim about content, so the entry is verified before it is
			// counted. It drives no mutation — hence a warning and a skip, not a
			// refusal — but leaving it out would make the SC-3 claim broader than
			// the code.
			full := filepath.Join(dir, e.Name())
			if cerr := security.CheckOwnedFile(full); cerr != nil {
				_ = security.WarnNotOurs(full, cerr)
				continue
			}
			n++
		}
	}
	return n
}
