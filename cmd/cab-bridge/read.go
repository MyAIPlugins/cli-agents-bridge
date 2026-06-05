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

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// ErrMessageNotFound is returned when no message with the requested id exists in
// the session's inbox/ or processed/. Mapped to exit 1 (default) by main.
var ErrMessageNotFound = errors.New("message not found")

// runRead implements `cab-bridge read <msg-id>`: print a single message's
// content body (or the full message with --json) located by id in the session's
// inbox/ (pending) or processed/ (consumed), WITHOUT consuming it. It replaces
// the `find ... | python3 -c "json.load()['content']"` dance needed to read a
// full body that `inbox --list` only previews (80 runes) and that `inspect`
// (manifest, not messages) cannot show (F-48). Pure read: nothing is moved or
// removed.
func runRead(args []string) error {
	fs_ := flag.NewFlagSet("read", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	sessionIDFlag := fs_.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	asJSON := fs_.Bool("json", false, "emit the full message as JSON on stdout (default: content body only)")
	if err := fs_.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	rest := fs_.Args()
	if len(rest) != 1 {
		return errors.New("read: expected exactly one positional argument <msg-id>")
	}
	msgID := rest[0]
	// F-37 safety net: reject a malformed/fabricated id before touching the FS.
	if err := message.ValidateMessageID(msgID); err != nil {
		return fmt.Errorf("read: %w", err)
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)
	sid, err := resolveCurrentSession(mgr, "read", *sessionIDFlag)
	if err != nil {
		return err
	}
	sessionDir := filepath.Join(cfg.DataDir, "sessions", sid)

	m, _, err := findMessage(sessionDir, msgID, cfg.MaxMessageBytes)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	mode := emitContent
	if *asJSON {
		mode = emitJSON
	}
	return emitMessage(enc, mode, m)
}

// findMessage scans the session's inbox/ then processed/ as a PURE READ and
// returns the first message whose decoded ID == msgID, plus the box it was found
// in. It matches on m.ID rather than the filename because processed/ files carry
// a MoveToProcessed timestamp prefix (<RFC3339>-msg-<id>.json) while inbox/ files
// do not — so a filename lookup would miss archived messages. This is the same
// decode-and-read policy as collectInbox. inbox/ has precedence: a still-pending
// copy is the more current state. A missing box contributes nothing; .tmp.*,
// non-.json, unreadable and malformed files are skipped. Returns
// ErrMessageNotFound when nothing matches.
func findMessage(sessionDir, msgID string, maxContentBytes int) (*message.Message, string, error) {
	for _, box := range []string{"inbox", "processed"} {
		dir := filepath.Join(sessionDir, box)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, "", fmt.Errorf("read: scan %s: %w", box, err)
		}
		for _, e := range entries {
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
			if m.ID == msgID {
				return m, box, nil
			}
		}
	}
	return nil, "", fmt.Errorf("read: %w: %q not in inbox/ or processed/", ErrMessageNotFound, msgID)
}
