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

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
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
	// State is what the RECIPIENT's mailbox says about this message, named for
	// what it actually proves (DESIGN §2.4). None of these attests that the work
	// is done — `archived` means a reply closed it, not that the task finished.
	State string `json:"state"`
}

// The states of §2.4. An I/O failure is deliberately NOT among them: it is an
// error, and collapsing it into a state would make a broken disk look like a
// fact about the message.
const (
	sentStateUnread   = "unread"   // in the recipient's inbox, never notified
	sentStateNotified = "notified" // handed to a next of theirs, not yet closed
	sentStateArchived = "archived" // closed by their reply
	sentStateUnknown  = "unknown"  // that session no longer exists
	sentStateExpired  = "expired"  // session is there, the message is not — retention took it
	// sentStateUnreadable: the file IS in their mailbox and cannot be decoded.
	// Not "expired" — nothing pruned it — and not an I/O error of ours either.
	sentStateUnreadable = "unreadable"
)

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

	sid, err := resolveCurrentSession(mgr, "sent", *sessionIDFlag)
	if err != nil {
		return err
	}

	sent, err := collectSent(filepath.Join(cfg.DataDir, "sessions", sid, "outbox"), cfg.MaxMessageBytes)
	if err != nil {
		return err
	}
	if err := annotateSentStates(cfg, mgr, sent); err != nil {
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
	fmt.Fprintln(tw, "MSG_ID\tTO\tTYPE\tSTATE\tTIMESTAMP\tIN_REPLY_TO")
	for _, row := range sent {
		inReplyTo := "-"
		if row.InReplyTo != nil {
			inReplyTo = *row.InReplyTo
		}
		// The agent NAME, not the raw id: this was the last surface still
		// showing opaque ids after an arc spent removing them.
		to := row.To
		if mf, err := mgr.LoadManifest(row.To); err == nil && mf.AgentName != "" {
			to = mf.AgentName
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", row.MsgID, to, row.Type, row.State, row.Timestamp, inReplyTo)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// A one-line gloss: the state names are precise but they do not explain
	// themselves, and none of them means "the work is done".
	fmt.Fprintln(os.Stdout, "\nunread = still in their inbox · notified = handed to their next · archived = they replied · expired/unreadable/unknown = see docs")
	return nil
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
		data, rerr := security.ReadOwnedFile(filepath.Join(outboxDir, name))
		if rerr != nil {
			_ = security.WarnNotOurs(filepath.Join(outboxDir, name), rerr)
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

// annotateSentStates fills in State for every row, scanning each recipient's
// mailbox EXACTLY ONCE.
//
// The naive shape — look up each sent message in the recipient's mailbox — is
// O(sent x mailbox), and worse than the complexity suggests: files in
// processed/ carry a timestamp prefix, so finding an id there means decoding
// them. Grouping by recipient first turns the whole report into one pass per
// distinct recipient.
//
// An I/O error propagates. It is not a state (§2.4): reporting a broken disk as
// if it were a fact about the message is how "gone" collapsed five different
// causes into one word.
func annotateSentStates(cfg config.Config, mgr *session.Manager, rows []sentSummary) error {
	byRecipient := map[string][]int{}
	for i, r := range rows {
		byRecipient[r.To] = append(byRecipient[r.To], i)
	}

	for to, idx := range byRecipient {
		index, err := buildMailboxIndex(cfg, mgr, to)
		if err != nil {
			return err
		}
		for _, i := range idx {
			if index == nil {
				rows[i].State = sentStateUnknown
				continue
			}
			if st, ok := index[rows[i].MsgID]; ok {
				rows[i].State = st
			} else {
				// The session is there but the message is not: retention pruned it.
				rows[i].State = sentStateExpired
			}
		}
	}
	return nil
}

// buildMailboxIndex maps message id -> state for ONE recipient, in a single
// scan of its inbox and processed dirs. A nil map (with nil error) means the
// session itself is gone.
func buildMailboxIndex(cfg config.Config, mgr *session.Manager, to string) (map[string]string, error) {
	if _, err := mgr.LoadManifest(to); err != nil {
		// ONLY a missing session is "unknown" (CRI diff-gate 1c P1-6). Any other
		// failure — permissions, a corrupt manifest, a disk giving up — is an
		// error and propagates: reporting it as a fact about the message is the
		// same confusion the old single word "gone" produced, and a disk that
		// fails is not a session that does not exist.
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("sent: load %s manifest: %w", to, err)
		}
		return nil, nil
	}
	sessionDir := filepath.Join(cfg.DataDir, "sessions", to)

	cursor, _, err := mgr.ReadWakeCursor(to)
	if err != nil {
		return nil, fmt.Errorf("sent: read %s wake cursor: %w", to, err)
	}

	index := map[string]string{}
	inbox, corrupt, _, err := readMailbox(filepath.Join(sessionDir, "inbox"), cfg.MaxMessageBytes)
	if err != nil {
		return nil, fmt.Errorf("sent: read %s inbox: %w", to, err)
	}
	// An unreadable file is PRESENT. Dropping it from the index made the message
	// fall through to "expired" — declaring a retention event that never
	// happened (LL-18 family). It is in the mailbox and it cannot be read: say
	// exactly that.
	for _, name := range corrupt {
		if id := archivedID(name); id != "" {
			index[id] = sentStateUnreadable
		}
	}
	for _, e := range inbox {
		if cursor.IsNotified(e.msg.ID) {
			index[e.msg.ID] = sentStateNotified
		} else {
			index[e.msg.ID] = sentStateUnread
		}
	}

	processed, err := os.ReadDir(filepath.Join(sessionDir, "processed"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return index, nil
		}
		return nil, fmt.Errorf("sent: read %s processed: %w", to, err)
	}
	for _, e := range processed {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		// The id is embedded in the archived filename (<timestamp>-<id>.json), so
		// the index is still built without DECODING a single archived file — but
		// the entry is verified before its name is believed. Trusting the name
		// alone let a planted file forge the "archived" state of a message, i.e.
		// tell a sender their peer had replied when nobody had. The check costs an
		// open+fstat and no read, keeping the optimisation that made this loop
		// worth writing.
		full := filepath.Join(sessionDir, "processed", name)
		if cerr := security.CheckOwnedFile(full); cerr != nil {
			_ = security.WarnNotOurs(full, cerr)
			continue
		}
		if id := archivedID(name); id != "" {
			index[id] = sentStateArchived
		}
	}
	return index, nil
}

// archivedID extracts msg-xxxxxxxxxxxx from a processed/ filename.
func archivedID(name string) string {
	base := strings.TrimSuffix(name, ".json")
	if i := strings.Index(base, "msg-"); i >= 0 {
		return base[i:]
	}
	return ""
}
