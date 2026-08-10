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
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// inboxPreviewMax is the rune budget for the content preview in `inbox --list`.
// Long enough to recognise a message at a glance, short enough to keep one row.
const inboxPreviewMax = 80

// inboxEntry is one row of `inbox --list`: a message sitting in the session's
// inbox/ (not archived yet) or processed/ (archived), read WITHOUT consuming it.
// Box distinguishes the two so an operator can tell "still to handle" from
// "already handled" — the recovery surface that completes F-30 (a reply
// archived to processed/ is now listable from home instead of grep-ing the
// sender's outbox or a fragile `ls inbox/*.json`).
type inboxEntry struct {
	Box           string `json:"box"` // "inbox" (not archived yet) or "processed" (archived)
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
	list := fs_.Bool("list", false, "list messages in inbox/ (not archived yet, read or unread) and processed/ (archived) WITHOUT consuming them")
	tidy := fs_.Bool("tidy", false, "archive every well-formed message currently in inbox/ to processed/ (use after --list: it sweeps what --list SHOWED; a message arriving later stays in inbox for the next pass)")
	asJSON := fs_.Bool("json", false, "emit JSON on stdout (default: human-readable)")
	if err := fs_.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// --list and --tidy are distinct, mutually exclusive modes of the same
	// subcommand; exactly one must be chosen.
	switch {
	case *list && *tidy:
		return fmt.Errorf("inbox: --list and --tidy are mutually exclusive — choose one")
	case !*list && !*tidy:
		return fmt.Errorf("inbox: nothing to do — pass --list to inspect or --tidy to archive inbox/ messages")
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	sid, err := resolveCurrentSession(mgr, "inbox", *sessionIDFlag)
	if err != nil {
		return err
	}
	sessionDir := filepath.Join(cfg.DataDir, "sessions", sid)

	if *tidy {
		res, err := tidyInbox(mgr, sid, sessionDir, cfg.MaxMessageBytes)
		if err != nil {
			return err
		}
		if *asJSON {
			out, merr := json.MarshalIndent(map[string]int{
				"tidied": res.moved, "openAsksLeft": res.openAsks, "unreadLeft": res.unread,
			}, "", "  ")
			if merr != nil {
				return fmt.Errorf("inbox: marshal: %w", merr)
			}
			fmt.Println(string(out))
		} else {
			fmt.Printf("tidied %d message(s) to processed/%s\n", res.moved, res.leftBehind())
		}
		return nil
	}

	entries, err := collectInbox(sessionDir, cfg.MaxMessageBytes)
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

// collectInbox reads the session's inbox/ (not archived yet) then processed/
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
			data, rerr := security.ReadOwnedFile(filepath.Join(dir, name))
			if rerr != nil {
				_ = security.WarnNotOurs(filepath.Join(dir, name), rerr)
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

// tidyResult is what one sweep did and what it deliberately did not do.
type tidyResult struct {
	moved    int
	openAsks int // NOTIFIED queries: work still owed to whoever asked
	unread   int // never emitted by any next: nobody has seen these
}

// leftBehind renders the second half of the summary. Empty when nothing was
// skipped, because a line about zero exceptions is noise on the common path.
func (r tidyResult) leftBehind() string {
	var parts []string
	if r.openAsks > 0 {
		parts = append(parts, fmt.Sprintf("%d open ask(s)", r.openAsks))
	}
	if r.unread > 0 {
		parts = append(parts, fmt.Sprintf("%d unread", r.unread))
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, " and ") + " left in place"
}

// tidyInbox is the F-22 --tidy sweep: it archives what the operator has HANDLED,
// which is what the help promises and what F-115 showed it was not doing.
//
// It moved every well-formed file, which cost two things that look different and
// are the same mistake — archiving what nobody handled:
//
//   - an OPEN ASK (a NOTIFIED query) went to processed/, so `reply` answered
//     "nothing to reply to" and the asker's `sent` showed `archived` with no
//     answer ever sent. Found by tidying my own inbox with a val's ask in it,
//     which is also why this comment exists;
//   - an UNREAD message — arrived seconds ago, emitted by no `next` — vanished
//     with NO TRACE ANYWHERE, and that one is worse: an open ask at least leaves
//     `archived` visible to the sender, while an unread `tell` is awaited by
//     nobody and was never seen by anybody. The val found this branch; I had
//     only found the first.
//
// One predicate covers both: archive only what was SHOWN (cursor.IsNotified) and
// is not an ask still owed an answer. Everything else — read tells, responses,
// already-closed queries — is swept as before, which is the case the command
// exists for.
//
// Skip, never refuse: the command must keep doing its job. What it left is
// stated in the summary, so "nothing happened to my ask" is never something the
// caller has to discover later.
//
// (The comment this replaces claimed it "sweeps what was VISIBLE" and that "a
// message that arrives afterwards stays in inbox". Neither was true: no
// visibility was ever consulted. Now both are.)
//
// Malformed, .tmp.*, or unreadable files are LEFT in inbox for forensics (same
// policy as consumeInboxEntry); processed/ is never touched. A missing or empty
// inbox yields 0, not an error. A genuine move failure (EXDEV/permission) is
// surfaced — never silently swallowed — with the count moved so far; a second
// --tidy retries the rest.
func tidyInbox(mgr *session.Manager, sid, sessionDir string, maxContentBytes int) (tidyResult, error) {
	var res tidyResult
	cursor, _, cerr := mgr.ReadWakeCursor(sid)
	if cerr != nil {
		return res, fmt.Errorf("inbox: read wake cursor: %w", cerr)
	}
	inboxDir := filepath.Join(sessionDir, "inbox")
	processedDir := filepath.Join(sessionDir, "processed")
	dirEntries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil // inbox not created yet — nothing to tidy
		}
		return res, fmt.Errorf("inbox: read inbox: %w", err)
	}

	for _, e := range dirEntries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		full := filepath.Join(inboxDir, name)
		data, rerr := security.ReadOwnedFile(full)
		if rerr != nil {
			_ = security.WarnNotOurs(full, rerr)
			continue // unreadable — leave in inbox for forensics
		}
		m, derr := message.DecodeLenient(data, maxContentBytes)
		if derr != nil {
			continue // malformed — leave in inbox (forensics), never archive blindly
		}
		// Never shown: not handled by definition, and it would disappear without
		// leaving a trace on either side.
		if !cursor.IsNotified(m.ID) {
			res.unread++
			continue
		}
		// Shown but still owed an answer. A query in inbox/ IS open: closing one
		// moves it out, so its presence here is the state, not a guess.
		if m.Type == message.TypeQuery {
			res.openAsks++
			continue
		}
		if err := transportfs.MoveToProcessed(full, processedDir); err != nil {
			return res, fmt.Errorf("inbox: tidy move %q (moved %d before failure): %w", full, res.moved, err)
		}
		res.moved++
	}
	return res, nil
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
