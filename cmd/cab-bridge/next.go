package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// next is the one command of the working loop (DESIGN v0.8 §2.2). It delivers
// every UNREAD message, marks the delivered ones NOTIFIED in the wake cursor,
// and NEVER moves a file — archiving belongs to `reply` alone (§2.3).
//
// Package invariant, exercised by TestNext_Invariant:
//
//	Only what was emitted is marked NOTIFIED, and everything UNREAD is emitted,
//	within the limits declared in the payload.
//
// Ordering is obligatory: PRINT FIRST, THEN COMMIT THE CURSOR. A crash between
// the two re-delivers (harmless, the model is at-least-once); the reverse order
// loses mail silently, and for a one-shot `tell` that loss would be permanent.

// nextStatus values carried in the payload.
const (
	nextStatusDelivered = "delivered"
	nextStatusTimeout   = "timeout"
	// nextStatusUnconfirmed means the messages were emitted but the cursor was
	// NOT committed because this instance lost wait ownership. They will be
	// delivered again to whoever owns the session now.
	nextStatusUnconfirmed = "unconfirmed"
)

type nextMessage struct {
	ID            string `json:"id"`
	From          string `json:"from"`
	FromAgentName string `json:"fromAgentName,omitempty"`
	FromRole      string `json:"fromRole,omitempty"`
	Type          string `json:"type"`
	Timestamp     string `json:"timestamp"`
	Bytes         int    `json:"bytes"`
	// Content is the message body, omitted when Oversize is set.
	Content string `json:"content,omitempty"`
	// Body is the on-disk path of the message, emitted INSTEAD of Content when
	// the message alone exceeds the page budget (§2.2): the body is already a
	// file, so a pointer beats a payload the harness might truncate.
	Body     string `json:"body,omitempty"`
	Oversize bool   `json:"oversize,omitempty"`
}

type nextPayload struct {
	Status       string        `json:"status"`
	Session      string        `json:"session"`
	Generation   int           `json:"generation"`
	Total        int           `json:"total"`
	Returned     int           `json:"returned"`
	HasMore      bool          `json:"hasMore"`
	CorruptCount int           `json:"corruptCount"`
	Corrupt      []string      `json:"corrupt,omitempty"`
	Messages     []nextMessage `json:"messages"`
	Warnings     []string      `json:"warnings,omitempty"`
	Hint         string        `json:"hint"`
}

// mailboxEntry is one decoded message file still sitting in inbox/.
type mailboxEntry struct {
	msg   *message.Message
	path  string
	bytes int
	when  time.Time
}

func runNext(args []string) error {
	// No flags at all (§2.2): not duration, not format, not filter, not
	// session-id. Every flag on this path costs the agent thinking on every
	// single cycle (§0). Tunables live in config; the session comes from cwd.
	if len(args) > 0 {
		return fmt.Errorf("next: takes no arguments or flags (session comes from the current directory, timings from config); got %q", args[0])
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	sid, err := resolveCurrentSession(mgr, "next", "")
	if err != nil {
		return err
	}
	return nextRun(context.Background(), mgr, cfg, sid, os.Stdout, os.Stderr)
}

// nextRun is the testable core: everything after the session is known.
func nextRun(parent context.Context, mgr *session.Manager, cfg config.Config, sid string, stdout, stderr io.Writer) error {
	// The full listen envelope (§3, F-95). receive --any had only half of it —
	// it waited without adopting the PID or beating, so a live waiter looked
	// STALE. next must be alive AND quiet, which is why the two commands merge.
	if err := mgr.AdoptPID(sid); err != nil {
		return fmt.Errorf("next: adopt session %s: %w", sid, err)
	}

	owner, err := mgr.ClaimListener(sid)
	if err != nil {
		return fmt.Errorf("next: claim wait ownership: %w", err)
	}
	ownerOK := func() bool { return mgr.IsListenerCurrent(sid, owner.Token) }

	window := time.Duration(cfg.WakeWindowHours) * time.Hour
	if window <= 0 {
		window = 24 * time.Hour
	}
	if err := mgr.SetListenUntil(sid, time.Now().UTC().Add(window)); err != nil {
		fmt.Fprintf(stderr, "warning: could not publish wait deadline: %v\n", err)
	}

	ctx, cancel := context.WithTimeout(parent, window)
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	sigDone := make(chan struct{})
	go func() {
		defer close(sigDone)
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()

	hbDone := mgr.StartHeartbeatOwned(ctx, sid, ownerOK)

	pollInterval := time.Duration(cfg.PollIntervalMs) * time.Millisecond
	if pollInterval <= 0 {
		pollInterval = time.Second
	}

	// Reclaim watcher: a resume elsewhere revokes us, and we must stop waiting
	// rather than deliver on behalf of the instance that replaced us.
	//
	// Its done channel is awaited before returning, per the package goroutine
	// discipline: the watcher writes to the caller's stderr, and a goroutine
	// still running after nextRun returned would race whoever owns that writer.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !ownerOK() {
					fmt.Fprintln(stderr, "wait ownership reclaimed by another instance — exiting")
					cancel()
					return
				}
			}
		}
	}()

	// One teardown, in the only order that terminates: cancel FIRST, then wait.
	// Awaiting a goroutine that exits on ctx.Done() before cancelling would
	// block until the 24h window elapsed.
	defer func() {
		cancel()
		<-watchDone
		<-sigDone
		<-hbDone
	}()

	inboxDir := filepath.Join(cfg.DataDir, "sessions", sid, "inbox")
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")

	for {
		payload, ready, err := collectNextPage(mgr, cfg, sid, inboxDir, owner.Generation)
		if err != nil {
			return err
		}
		if ready {
			// Best-effort pre-check: if ownership is ALREADY lost we must not
			// print a page labelled "delivered", because the cursor commit
			// below will refuse it and the messages belong to the new owner.
			// This does not close the window (ownership can still be revoked
			// between here and the commit) — that residual case is reported on
			// stderr, and `generation` in the payload lets a reader tell a late
			// orphan's output from a live one (§3, CRI's GO condition).
			if !ownerOK() {
				return enc.Encode(nextPayload{
					Status:     nextStatusUnconfirmed,
					Session:    sid,
					Generation: payload.page.Generation,
					Total:      payload.page.Total,
					Messages:   []nextMessage{},
					Hint:       "wait ownership was reclaimed by another instance — this run delivered nothing; the messages stay unread for the current owner",
				})
			}

			// PRINT FIRST — the emission is what the agent actually receives.
			if err := enc.Encode(payload.page); err != nil {
				return fmt.Errorf("next: emit: %w", err)
			}
			// THEN commit, re-checking ownership inside the lock.
			evicted, err := mgr.CommitWakeCursor(sid, payload.emittedIDs, time.Now().UTC(), ownerOK, payload.present)
			if err != nil {
				return fmt.Errorf("next: commit wake cursor: %w", err)
			}
			if evicted {
				fmt.Fprintln(stderr, "wait ownership was reclaimed while emitting — delivery left unconfirmed, these messages stay unread for the current owner")
			}
			return nil
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				// Timeout is exit 0 with a payload, never 124 (F-24): an idle
				// window is not a failure, and a "command failed" every cycle
				// trains the agent to ignore real errors.
				return enc.Encode(nextPayload{
					Status:   nextStatusTimeout,
					Session:  sid,
					Messages: []nextMessage{},
					Hint:     "nothing arrived within the wait window — run next again",
				})
			}
			return nil
		case <-time.After(pollInterval):
		}
	}
}

// pageResult bundles what one collection pass produced.
type pageResult struct {
	page       nextPayload
	emittedIDs []string
	present    map[string]bool
}

// collectNextPage reads the mailbox and builds one bounded page of UNREAD
// messages. ready is false when there is nothing to deliver yet.
func collectNextPage(mgr *session.Manager, cfg config.Config, sid, inboxDir string, generation int) (pageResult, bool, error) {
	cursor, cursorWarn, err := mgr.ReadWakeCursor(sid)
	if err != nil {
		return pageResult{}, false, fmt.Errorf("next: read wake cursor: %w", err)
	}

	entries, corrupt, err := readMailbox(inboxDir, cfg.MaxMessageBytes)
	if err != nil {
		return pageResult{}, false, fmt.Errorf("next: read mailbox: %w", err)
	}

	present := make(map[string]bool, len(entries))
	var unread []mailboxEntry
	for _, e := range entries {
		present[e.msg.ID] = true
		if !cursor.IsNotified(e.msg.ID) {
			unread = append(unread, e)
		}
	}
	if len(unread) == 0 {
		return pageResult{}, false, nil
	}

	// Deterministic order: decoded timestamp, id as tie-break. os.ReadDir
	// returns lexical order over random ids, which is NOT arrival order.
	sort.Slice(unread, func(i, j int) bool {
		if unread[i].when.Equal(unread[j].when) {
			return unread[i].msg.ID < unread[j].msg.ID
		}
		return unread[i].when.Before(unread[j].when)
	})

	maxCount := cfg.MaxPageMessages
	if maxCount <= 0 {
		maxCount = 50
	}
	maxBytes := cfg.MaxPageBytes
	if maxBytes <= 0 {
		maxBytes = 131072
	}

	page := nextPayload{
		Status:       nextStatusDelivered,
		Session:      sid,
		Generation:   generation,
		Total:        len(unread),
		CorruptCount: len(corrupt),
		Messages:     []nextMessage{},
	}
	for _, name := range corrupt {
		page.Corrupt = append(page.Corrupt, name)
	}
	if cursorWarn != "" {
		page.Warnings = append(page.Warnings, cursorWarn)
	}
	if len(corrupt) > 0 {
		// Declared, never silently skipped (§2.7): a corrupt file must neither
		// block next forever nor vanish without a trace.
		page.Warnings = append(page.Warnings, fmt.Sprintf("%d unreadable file(s) left in inbox for inspection", len(corrupt)))
	}

	var (
		emitted []string
		used    int
	)
	for _, e := range unread {
		if len(page.Messages) >= maxCount {
			break
		}
		// A single message over budget goes out alone as a pointer rather than
		// starving forever behind a limit it can never fit under.
		if e.bytes > maxBytes {
			if len(page.Messages) > 0 {
				break
			}
			page.Messages = append(page.Messages, newNextMessage(e, true))
			emitted = append(emitted, e.msg.ID)
			break
		}
		if used+e.bytes > maxBytes && len(page.Messages) > 0 {
			break
		}
		page.Messages = append(page.Messages, newNextMessage(e, false))
		emitted = append(emitted, e.msg.ID)
		used += e.bytes
	}

	page.Returned = len(page.Messages)
	page.HasMore = page.Returned < page.Total
	// The output declares its own next action, so the agent never looks for a
	// --page flag that does not exist (§2.7).
	if page.HasMore {
		page.Hint = "hasMore: true — the rest arrives with the next run of next"
	} else {
		page.Hint = "run next again to stay reachable"
	}

	return pageResult{page: page, emittedIDs: emitted, present: present}, true, nil
}

func newNextMessage(e mailboxEntry, oversize bool) nextMessage {
	m := nextMessage{
		ID:            e.msg.ID,
		From:          e.msg.From,
		FromAgentName: e.msg.FromAgentName,
		FromRole:      e.msg.FromRole,
		Type:          e.msg.Type,
		Timestamp:     e.msg.Timestamp,
		Bytes:         e.bytes,
	}
	if oversize {
		m.Oversize = true
		m.Body = e.path
		return m
	}
	m.Content = e.msg.Content
	return m
}

// readMailbox decodes every message still in inbox/ and reports the files it
// could not decode instead of skipping them silently (collectInbox does skip —
// inbox.go:131-143 — which is exactly the behaviour §2.7 rules out).
func readMailbox(inboxDir string, maxContentBytes int) ([]mailboxEntry, []string, error) {
	dir, err := os.ReadDir(inboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var (
		entries []mailboxEntry
		corrupt []string
	)
	for _, de := range dir {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if filepath.Ext(name) != ".json" || strings.HasPrefix(name, ".tmp.") {
			continue
		}
		full := filepath.Join(inboxDir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			corrupt = append(corrupt, name)
			continue
		}
		m, err := message.DecodeLenient(data, maxContentBytes)
		if err != nil {
			corrupt = append(corrupt, name)
			continue
		}
		entries = append(entries, mailboxEntry{
			msg:   m,
			path:  full,
			bytes: len(data),
			when:  parseMessageTime(m.Timestamp),
		})
	}
	sort.Strings(corrupt)
	return entries, corrupt, nil
}

// parseMessageTime decodes the RFC3339 timestamp. An unparsable one sorts to
// the zero time, i.e. first: a message with a broken timestamp is shown early
// rather than starved behind well-formed ones (§2.7 asks for a declared policy).
func parseMessageTime(ts string) time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
