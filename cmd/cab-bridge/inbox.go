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

// inboxPreviewMax is the rune budget for the content preview in `inbox --list`.
// Long enough to recognise a message at a glance, short enough to keep one row.
const inboxPreviewMax = 80

// inboxEntry is one row of `inbox --list`: a message sitting in the session's
// inbox/ (pending) or processed/ (already consumed), read WITHOUT consuming it.
// Box distinguishes the two so an operator can tell "still to handle" from
// "already handled" — the recovery surface that completes F-30 (a reply
// archived to processed/ is now listable from home instead of grep-ing the
// sender's outbox or a fragile `ls inbox/*.json`).
type inboxEntry struct {
	Box           string `json:"box"` // "inbox" (pending) or "processed" (consumed)
	MsgID         string `json:"msgId"`
	From          string `json:"from"`
	FromAgentName string `json:"fromAgentName"`
	Type          string `json:"type"`
	Timestamp     string `json:"timestamp"`
	Preview       string `json:"preview"`
}

func runInbox(args []string) error {
	fs_ := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	sessionIDFlag := fs_.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	list := fs_.Bool("list", false, "list messages in inbox/ (pending) and processed/ (consumed) WITHOUT consuming them")
	asJSON := fs_.Bool("json", false, "emit JSON array on stdout (default: human tabwriter)")
	if err := fs_.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// --list is the only mode today; require it explicitly so a future mode
	// (e.g. --tidy) can be added without changing this default.
	if !*list {
		return fmt.Errorf("inbox: nothing to do — pass --list to list inbox/ and processed/ messages")
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

	entries, err := collectInbox(filepath.Join(cfg.DataDir, "sessions", sid), cfg.MaxMessageBytes)
	if err != nil {
		return err
	}

	if *asJSON {
		out, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("inbox: marshal: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BOX\tMSG_ID\tFROM\tAGENT\tTYPE\tTIMESTAMP\tPREVIEW")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Box, e.MsgID, e.From, e.FromAgentName, e.Type, e.Timestamp, e.Preview)
	}
	return tw.Flush()
}

// collectInbox reads the session's inbox/ (pending) then processed/ (consumed)
// dirs as a PURE READ — it never moves or deletes a file, so `inbox --list` is
// guaranteed non-consuming. Returns one entry per message, inbox/ first. A
// missing dir contributes no entries (lazy-created; not an error). The returned
// slice is empty-not-nil so --json emits [] not null (BUG-B). Unreadable,
// malformed, or .tmp.* files are skipped silently, consistent with the other
// read-only listing path (collectSent).
func collectInbox(sessionDir string, maxContentBytes int) ([]inboxEntry, error) {
	out := []inboxEntry{}
	for _, box := range []string{"inbox", "processed"} {
		dir := filepath.Join(sessionDir, box)
		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // box not created yet — no messages here
			}
			return nil, fmt.Errorf("inbox: read %s: %w", box, err)
		}
		for _, e := range dirEntries {
			name := e.Name()
			if e.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(dir, name))
			if rerr != nil {
				continue
			}
			m, derr := message.DecodeLenient(data, maxContentBytes)
			if derr != nil {
				continue
			}
			out = append(out, inboxEntry{
				Box:           box,
				MsgID:         m.ID,
				From:          m.From,
				FromAgentName: m.FromAgentName,
				Type:          m.Type,
				Timestamp:     m.Timestamp,
				Preview:       previewContent(m.Content, inboxPreviewMax),
			})
		}
	}
	return out, nil
}

// previewContent collapses a message body to a single scannable line: runs of
// whitespace (including newlines) become single spaces, and the result is
// truncated to maxRunes with a trailing "..." marker when it overflows.
// Rune-based so multi-byte content is never cut mid-character.
func previewContent(content string, maxRunes int) string {
	collapsed := strings.Join(strings.Fields(content), " ")
	r := []rune(collapsed)
	if len(r) <= maxRunes {
		return collapsed
	}
	return string(r[:maxRunes]) + "..."
}
