package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// The inbox as the MCP tools see it. `next` over MCP only READS (nothing is marked,
// no cursor moves), so a result that never arrives loses nothing; messages leave the
// inbox only by an explicit act of the agent: ack of ids, or reply to an ask.

// errInboxScanTimeout is returned when the inbox could not be read within the budget
// of the call. It is an error and never a partial result: a half-read inbox shown as
// the inbox is how a message goes missing without anybody being told.
var errInboxScanTimeout = errors.New("the inbox could not be read within the time allowed for one call")

// errMessageNotFound is returned by locateMessage for an id that is in neither the
// inbox nor processed/.
var errMessageNotFound = errors.New("no such message in the inbox or in processed/")

// maxAckBatch bounds one ack: a batch is validated as a whole before the first
// message moves, so its size is bounded.
const maxAckBatch = 20

// scanGuard stops scans from piling up behind one that is slow: a second scan of
// the same inbox while the first still runs is refused as a timeout instead of
// doubling the work.
type scanGuard struct{ busy atomic.Bool }

// inboxScan is one read of the inbox.
type inboxScan struct {
	entries []mailboxEntry
	corrupt []string
}

// scanInboxWithin reads inbox/ with readMailbox, giving up when ctx ends. A file in
// the inbox that does not belong to this user fails the scan outright, the same
// policy `next` has: delivering from a mailbox that holds a file nobody can vouch
// for is the one thing worth stopping for.
func scanInboxWithin(ctx context.Context, guard *scanGuard, inboxDir string, maxContentBytes int) (inboxScan, error) {
	if !guard.busy.CompareAndSwap(false, true) {
		return inboxScan{}, errInboxScanTimeout
	}
	type outcome struct {
		scan inboxScan
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		defer guard.busy.Store(false)
		entries, corrupt, foreign, err := readMailbox(inboxDir, maxContentBytes)
		switch {
		case err != nil:
			done <- outcome{err: err}
		case len(foreign) > 0:
			done <- outcome{err: fmt.Errorf("refusing to read an inbox that holds files not owned by this user: %w (%d file(s); inspect them with the CLI)", security.ErrOwnershipMismatch, len(foreign))}
		default:
			done <- outcome{scan: inboxScan{entries: entries, corrupt: corrupt}}
		}
	}()
	select {
	case o := <-done:
		return o.scan, o.err
	case <-ctx.Done():
		return inboxScan{}, errInboxScanTimeout
	}
}

// located says where a message was found.
type located struct {
	path     string
	archived bool
}

// locateMessage finds a message by id WITHOUT decoding every file: in inbox/ the file
// is named <id>.json, in processed/ it is <timestamp>-<id>.json. The id is validated
// against the wire format before it is used to build any path.
func locateMessage(sessionDir, id string) (located, error) {
	if err := message.ValidateMessageID(id); err != nil {
		return located{}, err
	}
	inboxPath := filepath.Join(sessionDir, "inbox", id+".json")
	if _, err := os.Lstat(inboxPath); err == nil {
		return located{path: inboxPath}, nil
	} else if !os.IsNotExist(err) {
		return located{}, fmt.Errorf("look for %s in the inbox: %w", id, err)
	}
	processedDir := filepath.Join(sessionDir, "processed")
	entries, err := os.ReadDir(processedDir)
	if err != nil {
		if os.IsNotExist(err) {
			return located{}, errMessageNotFound
		}
		return located{}, fmt.Errorf("look for %s in processed/: %w", id, err)
	}
	suffix := "-" + id + ".json"
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		path := filepath.Join(processedDir, e.Name())
		// A name alone must not forge "archived": the entry has to be ours.
		if cerr := security.CheckOwnedFile(path); cerr != nil {
			continue
		}
		return located{path: path, archived: true}, nil
	}
	return located{}, errMessageNotFound
}

// loadMessageAt reads and decodes one message file, verifying it belongs to this user.
func loadMessageAt(path string, maxContentBytes int) (*message.Message, int, error) {
	data, err := security.ReadOwnedFile(path)
	if err != nil {
		return nil, 0, err
	}
	m, err := message.DecodeLenient(data, maxContentBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return m, len(data), nil
}

// ackOutcome is what happened to one id of an ack.
type ackOutcome string

const (
	ackArchived        ackOutcome = "archived"
	ackAlreadyArchived ackOutcome = "already-archived"
	ackRefused         ackOutcome = "refused"
	ackFailed          ackOutcome = "failed"
	ackNotAttempted    ackOutcome = "not-attempted"
)

// ackResult is one id's outcome, with a reason when it was refused. The reason never
// quotes a message.
type ackResult struct {
	ID      string     `json:"id"`
	Outcome ackOutcome `json:"outcome"`
	Detail  string     `json:"detail,omitempty"`
}

// ackCheck decides whether the message in the inbox may be archived by an ack. It is
// where the caller puts "visible to Desktop" and "not an ask": it receives the
// decoded message and the size of its file.
type ackCheck func(m *message.Message, size int) error

// archiveBatch archives exactly the named messages, in two phases.
//
// Phase one validates EVERY id (grammar, existence, the caller's check) and moves
// nothing; if a single id is refused the whole batch is refused, with a result per
// id, so a mistake in one position can never read as a completed ack. Phase two moves
// them one by one; a crash or an error between two moves leaves a completed prefix and
// the retry is idempotent, because a message that is no longer in inbox/ but is in
// processed/ is "already archived" (MoveToProcessed alone does not know the
// difference: it only fails with ENOENT). On an error it stops, and says so for the rest.
//
// move is injectable so the crash between two archivings can be tested.
func archiveBatch(sessionDir string, ids []string, maxContentBytes int, check ackCheck, move func(src, processedDir string) error) ([]ackResult, bool) {
	ids = dedupe(ids)
	if len(ids) == 0 {
		return []ackResult{{Outcome: ackRefused, Detail: "no ids given"}}, false
	}
	if len(ids) > maxAckBatch {
		return []ackResult{{Outcome: ackRefused, Detail: fmt.Sprintf("at most %d ids per ack, got %d", maxAckBatch, len(ids))}}, false
	}

	results := make([]ackResult, len(ids))
	paths := make([]string, len(ids))
	allOK := true
	for i, id := range ids {
		results[i] = ackResult{ID: id}
		loc, err := locateMessage(sessionDir, id)
		switch {
		case errors.Is(err, errMessageNotFound):
			results[i].Outcome, results[i].Detail = ackRefused, "no such message"
			allOK = false
		case err != nil:
			results[i].Outcome, results[i].Detail = ackRefused, "not a valid message id"
			allOK = false
		case loc.archived:
			results[i].Outcome = ackAlreadyArchived
		default:
			m, size, lerr := loadMessageAt(loc.path, maxContentBytes)
			if lerr != nil {
				results[i].Outcome, results[i].Detail = ackRefused, "the message could not be read"
				allOK = false
				break
			}
			if cerr := check(m, size); cerr != nil {
				results[i].Outcome, results[i].Detail = ackRefused, cerr.Error()
				allOK = false
				break
			}
			paths[i] = loc.path
		}
	}
	if !allOK {
		for i := range results {
			if results[i].Outcome == "" {
				results[i].Outcome, results[i].Detail = ackNotAttempted, "the batch was refused as a whole; nothing was moved"
			}
		}
		return results, false
	}

	processedDir := filepath.Join(sessionDir, "processed")
	failed := false
	for i := range results {
		if results[i].Outcome == ackAlreadyArchived {
			continue
		}
		if failed {
			results[i].Outcome, results[i].Detail = ackNotAttempted, "an earlier archiving failed; retry the ack"
			continue
		}
		if err := move(paths[i], processedDir); err != nil {
			results[i].Outcome, results[i].Detail = ackFailed, "could not be archived"
			failed = true
			continue
		}
		results[i].Outcome = ackArchived
	}
	return results, !failed
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// moveToProcessed is the production mover.
func moveToProcessed(src, processedDir string) error {
	return transportfs.MoveToProcessed(src, processedDir)
}
