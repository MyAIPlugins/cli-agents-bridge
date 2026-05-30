package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// sentSummary is one row of `cab sent`: a message this session has sent, read
// back from its own outbox (F-9). It answers "what did I send and to whom" from
// the sender's OWN data — the gap that forced an orchestrator to inspect the
// recipient's inbox to confirm its own sends.
type sentSummary struct {
	MsgID     string  `json:"msgId"`
	To        string  `json:"to"`
	Type      string  `json:"type"`
	Timestamp string  `json:"timestamp"`
	InReplyTo *string `json:"inReplyTo,omitempty"`
}

func runSent(args []string) error {
	fs_ := flag.NewFlagSet("sent", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	sessionIDFlag := fs_.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	asJSON := fs_.Bool("json", false, "emit JSON array on stdout (default: human tabwriter)")
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

	sid, err := resolveSessionID(mgr, *sessionIDFlag)
	if err != nil {
		return err
	}

	sent, err := collectSent(filepath.Join(cfg.DataDir, "sessions", sid, "outbox"), cfg.MaxMessageBytes)
	if err != nil {
		return err
	}

	if *asJSON {
		out, err := json.MarshalIndent(sent, "", "  ")
		if err != nil {
			return fmt.Errorf("sent: marshal: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MSG_ID\tTO\tTYPE\tTIMESTAMP\tIN_REPLY_TO")
	for _, s := range sent {
		inReplyTo := "-"
		if s.InReplyTo != nil {
			inReplyTo = *s.InReplyTo
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.MsgID, s.To, s.Type, s.Timestamp, inReplyTo)
	}
	return tw.Flush()
}

// collectSent reads the sender's outbox and returns one summary per message in
// os.ReadDir (lexical) order. A missing outbox yields an empty slice (BUG-B:
// empty not nil, so --json emits [] not null). Unreadable/malformed/.tmp files
// are skipped silently, consistent with the inbox consume policy.
func collectSent(outboxDir string, maxContentBytes int) ([]sentSummary, error) {
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []sentSummary{}, nil
		}
		return nil, fmt.Errorf("sent: read outbox: %w", err)
	}

	out := []sentSummary{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(outboxDir, name))
		if rerr != nil {
			continue
		}
		m, derr := message.DecodeLenient(data, maxContentBytes)
		if derr != nil {
			continue
		}
		out = append(out, sentSummary{
			MsgID:     m.ID,
			To:        m.To,
			Type:      m.Type,
			Timestamp: m.Timestamp,
			InReplyTo: m.InReplyTo,
		})
	}
	return out, nil
}
