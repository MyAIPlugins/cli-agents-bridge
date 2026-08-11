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
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	"github.com/myAIPlugins/cli-agents-bridge/internal/shellarg"
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

// Statuses. next emits JSONL: a PAGE record, then a COMMIT record.
//
// The page record says "emitted", a fact that is true when it is written. It
// must never claim "delivered"/"confirmed": with emit-before-commit no single
// JSON can know its own future (CRI diff-gate P0-2), and an agent believes the
// structured payload over a later stderr line — so a page that self-certifies
// produces two instances acting on the same brief.
const (
	nextStatusEmitted = "emitted"

	// Commit-record statuses: the actual outcome, written after the fact.
	nextStatusCommitted    = "committed"
	nextStatusNotCommitted = "not-committed"
	// nextStatusInterrupted: the wait ended without a delivery (a signal, or a
	// parent that cancelled). Not a failure and not a timeout — there is no
	// timeout any more — but it must not be silent either.
	nextStatusInterrupted = "interrupted"
)

// Hints for the not-committed cases. They are constants because their wording
// IS the contract with the agent: it must contradict the page just emitted
// (an LLM believes structured output over a later log line), forbid the re-run
// its instinct suggests — which would steal the wait from the instance that
// replaced this one — and never use internal vocabulary like "wait ownership",
// a concept the agent has never been shown.
const (
	hintTakeoverBeforeEmit = "another instance of this session took over while you were waiting: nothing was delivered to you. " +
		"Do NOT run next again from here — the other instance is waiting and these messages will reach it."

	hintTakeoverAfterEmit = "IGNORE the emitted page above — treat those messages as NOT received. " +
		"Another instance of this session took over while you were reading; do NOT run next again from here, the messages will reach it."

	hintCommitFailed = "IGNORE the emitted page above — treat those messages as NOT received. " +
		"They stay unread and will come back on the next run."
)

type nextMessage struct {
	// Redelivered marks a message this session had already been shown: by a join
	// after a crash or restart, or by a `reply` that left the ask open and put it
	// back in line (F-109). §2.3 asks for the marker INLINE: the join line that
	// announced the replay is on another command's stderr, minutes earlier and
	// without ids, which is exactly the correlation-at-a-distance the inline
	// field exists to avoid (CRI2 P1-3).
	Redelivered   bool   `json:"redelivered,omitempty"`
	ID            string `json:"id"`
	From          string `json:"from"`
	FromAgentName string `json:"fromAgentName,omitempty"`
	// FromScope appears ONLY when the sender's project differs from the reader's
	// (F-116). In-scope it would be noise on every message; cross-scope it is the
	// one thing that says "this did not come from your own team".
	//
	// Provenance, not authentication: it reports what the sender DECLARED when it
	// wrote. Empty on messages written before the field existed — and absence is
	// "not stated", never "same project as you", which is why nothing here fills
	// the gap by looking the sender up now. That would answer a different
	// question (where it is NOW) under the label of this one.
	FromScope string `json:"fromScope,omitempty"`
	// FromAddress is the token that writes BACK to this sender —
	// `VAL-payload@/Users/alan/develop/payload`. Present only when FromScope is,
	// i.e. only when the plain name would not reach them.
	//
	// Composing is not transcribing, so assembling it from the two fields above
	// would not have broken the rule that an agent never re-types an identifier.
	// It would only have made the agent THINK, which is the thing this whole arc
	// keeps removing: it copies, it does not assemble.
	//
	// The FULL path, not the basename, for the reason the scope column shortens
	// and this does not: here there is no list of the other scopes, so ambiguity
	// cannot be detected — and an ambiguous abbreviation is worse than a long
	// exact one. This token always resolves.
	//
	// LOGICAL value: parse it, compare it, pass it to an API. To paste it into a
	// shell use FromAddressShellArg below — this one is not quoted, and a project
	// path may contain a space or an apostrophe.
	FromAddress string `json:"fromAddress,omitempty"`
	// FromAddressShellArg is FromAddress rendered as ONE POSIX shell argument.
	//
	// CONTRACT: the decoded JSON value is a single shell word for POSIX `sh` (and
	// for bash/zsh); evaluating it yields exactly FromAddress as one argv entry,
	// with no globbing, no substitution and no side effects. A token that needs
	// no quoting is byte-identical to FromAddress, so the ordinary message reads
	// the same as before.
	//
	// A SEPARATE FIELD rather than quoting FromAddress in place, and the reason is
	// not tidiness: FromAddress is documented as the pastable value in skills that
	// live OUTSIDE this repository, which no gate can read and no merge can
	// update. Changing its meaning would have a window — between the merge and
	// whenever those files caught up — in which the instruction in circulation is
	// actively wrong. An added field has no such instant.
	//
	// It exists at all because "always put it in quotes", which is what one skill
	// told agents to do, is not merely inconvenient — it is WRONG: it holds for a
	// space and breaks on an apostrophe, since the reader closes the quote they
	// opened (`Alan's Project` → `unmatched '`). The algorithm belongs in the
	// renderer, not in the reader.
	//
	// OUT OF CONTRACT: a tab or newline in the path round-trips through the shell
	// correctly but breaks any line-oriented surface it is printed on. Values are
	// safe; displays are not.
	//
	// Derived from the SAME recipient as FromAddress, right next to it, so the two
	// cannot drift: there is one datum, rendered twice.
	FromAddressShellArg string `json:"fromAddressShellArg,omitempty"`
	FromRole            string `json:"fromRole,omitempty"`
	Type                string `json:"type"`
	Timestamp           string `json:"timestamp"`
	Bytes               int    `json:"bytes"`
	// Content is the message body, omitted when Oversize is set.
	Content string `json:"content,omitempty"`
	// BodyFile is the on-disk PATH of the message, emitted INSTEAD of Content
	// when the message alone exceeds the page budget (§2.2): the body is already
	// a file, so a pointer beats a payload the harness might truncate. Named
	// bodyFile, not body — a field called "body" that holds a path invites the
	// agent to print it as the message.
	BodyFile string `json:"bodyFile,omitempty"`
	Oversize bool   `json:"oversize,omitempty"`
	// Note carries the instruction inline, next to the field it is about: an
	// agent reading one message must not have to correlate it with a hint at
	// the top of the page to work out that the body is a path, not the text.
	Note string `json:"note,omitempty"`
	// Closes lists the asks this reply archived. It exists on the message
	// already; what was missing is that it never reached the READER.
	//
	// F-109: a reply closes one DELIVERY of open asks from that sender, and the
	// oldest open delivery can still be one the agent never read — so a message
	// sent while the other side was working can be archived by an answer that
	// never considered it. It cannot close several deliveries at once any more,
	// but it is not impossible, and the sender used to see "reply received,
	// openAsks 0" with no way to know WHICH ask had just closed. Carrying it
	// is what lets them recognise an id they were not expecting, without going to
	// look for it: the alternative was a rule telling agents to re-read their own
	// output, which is the shape Alan ruled out.
	Closes []string `json:"closes,omitempty"`
}

// outboundAsk is one of MY asks still waiting for an answer.
type outboundAsk struct {
	MsgID string `json:"msgId"`
	To    string `json:"to"`
	Age   string `json:"age"`
	State string `json:"state"`
}

type nextPayload struct {
	Status  string `json:"status"`
	Session string `json:"session"`
	// AgentName is here because an eight-hex id is something you RECOGNISE and a
	// name is something you READ. A val re-armed three times in a row onto the
	// wrong session without noticing: `679b7060` said nothing, `ESC-bridge`
	// would have stopped it at the first. It is not a flag and not a choice —
	// one more word on an output the agent reads anyway.
	AgentName    string        `json:"agentName,omitempty"`
	Generation   int           `json:"generation"`
	Total        int           `json:"total"`
	Returned     int           `json:"returned"`
	HasMore      bool          `json:"hasMore"`
	CorruptCount int           `json:"corruptCount"`
	Corrupt      []string      `json:"corrupt,omitempty"`
	Messages     []nextMessage `json:"messages"`
	Warnings     []string      `json:"warnings,omitempty"`
	// Outbound and OpenAsks are the summary §2.2 asks for, and they are here
	// rather than in a command of their own on purpose: `next` is run anyway, so
	// this is information at zero cost instead of one more thing to choose.
	//
	// They are what stitches back the only thing lost by removing the ACKs —
	// otherwise "did they get my brief?" would require REMEMBERING to run `sent`,
	// i.e. spending the scarce resource. Outbound counts only asks: a tell is
	// fire-and-forget, and a counter that grows forever is a counter the agent
	// learns to ignore.
	Outbound []outboundAsk `json:"outbound,omitempty"`
	OpenAsks int           `json:"openAsks"`
	Hint     string        `json:"hint"`
}

// nextCommitRecord is the second JSONL record: what actually happened to the
// cursor. It is the only record allowed to state an outcome.
type nextCommitRecord struct {
	Status     string `json:"status"`
	Session    string `json:"session"`
	AgentName  string `json:"agentName,omitempty"`
	Generation int    `json:"generation"`
	// Confirmed is always an array, never null: a consumer that iterates it
	// should not have to special-case the paths that confirm nothing.
	Confirmed []string `json:"confirmed"`
	// Outbound rides on the interrupted record too. That path is the complement
	// of the emitting one, and it is where "did my brief arrive?" is asked
	// hardest: the agent asked, waited, and got nothing back. openAsks is NOT
	// repeated here — no page was emitted, so that number has not changed and
	// restating it would be noise.
	Outbound []outboundAsk `json:"outbound,omitempty"`
	Hint     string        `json:"hint"`
}

// newCommitRecord keeps Confirmed non-nil for every exit path.
func newCommitRecord(status, sid string, generation int, confirmed []string, hint string) nextCommitRecord {
	if confirmed == nil {
		confirmed = []string{}
	}
	return nextCommitRecord{Status: status, Session: sid, Generation: generation, Confirmed: confirmed, Hint: hint}
}

// withName stamps the agent name on a record. Kept separate so every exit path
// gets it without threading a manager through the constructor.
func withName(rec nextCommitRecord, name string) nextCommitRecord {
	rec.AgentName = name
	return rec
}

// mailboxEntry is one decoded message file still sitting in inbox/.
type mailboxEntry struct {
	msg      *message.Message
	path     string
	bytes    int
	when     time.Time
	replayed bool
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
	// Adopt + claim as ONE locked operation: doing them separately lets a
	// concurrent `register --resume` be defeated by the waiter it just evicted
	// (CRI diff-gate P0-1).
	me := myName(mgr, sid)

	owner, err := mgr.StartWait(sid)
	if err != nil {
		return fmt.Errorf("next (%s): %w", whoIThoughtIWas(mgr, sid), err)
	}
	ownerOK := func() bool { return mgr.IsListenerCurrent(sid, owner.Token) }

	// NO WINDOW, by contract (DESIGN §2.2, rev. cdb21dc). A window that expires
	// is the waiter dismissing itself, and only Alan closes a session: an agent
	// that stops listening on its own has delegated that decision to a timer.
	//
	// So the context is cancellable but never scheduled: the run ends on a
	// delivery, a signal, or a reclaim — never on time.
	//
	// Nothing is published here to say "I am waiting", and nothing is cleared on
	// the way out. StartWait above already wrote the only record that can answer
	// it — the ownership claim, under the session lock — and overview reads it
	// through ListenerOwner.Listening. The marker this replaces was cleared by a
	// deferred write that ran AFTER an eviction, on behalf of the instance that
	// had replaced us: the exit path speaking for the live one.
	ctx, cancel := context.WithCancel(parent)
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
			// owner-check -> emit -> commit is ONE critical section against
			// claim/reclaim (CRI diff-gate 1c P1-2). With the check outside it, a
			// waiter evicted right after the check still printed its page and woke
			// its agent: two instances woken by the same brief, which §3 rules out
			// because it means two replies and two external effects. The output is
			// bounded by the page limits, so the section stays short.
			//
			// The session lock is re-entrant within a process, so the commit below
			// re-acquires it harmlessly.
			var evicted bool
			payload.page.AgentName = me
			emitErr := mgr.WithSessionLock(sid, func() error {
				if !ownerOK() {
					evicted = true
					return nil
				}
				if err := enc.Encode(payload.page); err != nil {
					return fmt.Errorf("emit: %w", err)
				}
				var cerr error
				evicted, cerr = mgr.CommitWakeCursor(sid, payload.emittedIDs, time.Now().UTC(), ownerOK, payload.present)
				return cerr
			})

			switch {
			case emitErr != nil:
				_ = enc.Encode(withName(newCommitRecord(nextStatusNotCommitted, sid, payload.page.Generation, nil, hintCommitFailed), me))
				return fmt.Errorf("next: %w", emitErr)
			case evicted:
				_ = enc.Encode(withName(newCommitRecord(nextStatusNotCommitted, sid, payload.page.Generation, nil, hintTakeoverBeforeEmit), me))
				return errors.New("next: another instance of this session took over")
			default:
				return enc.Encode(withName(newCommitRecord(nextStatusCommitted, sid, payload.page.Generation, payload.emittedIDs, "these messages are yours; run next again to stay reachable"), me))
			}
		}

		select {
		case <-ctx.Done():
			// Reached only via a signal, a reclaim or a cancelled parent — never
			// by elapsed time. It still gets a RECORD (CRI2 P1-1): exiting 0 with
			// zero bytes leaves a wrapper unable to tell "interrupted while
			// waiting" from "nothing happened at all", and every other exit path
			// here says what it did.
			rec := withName(newCommitRecord(nextStatusInterrupted, sid, owner.Generation, nil,
				"the wait was interrupted before anything arrived — nothing was delivered; run next again when you are ready"), me)
			rec.Outbound = collectOutboundAsks(mgr, cfg, sid)
			return enc.Encode(rec)
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
	// My own project, to decide which messages are worth labelling as foreign.
	// A failure here is not worth refusing a delivery over: the label is dropped,
	// never guessed.
	// TWO facts, not one: which scope is mine, and whether I know it. Collapsing
	// them into an empty string made a failed manifest read say "your scope is
	// nothing", so every modern message compared unequal and came out labelled
	// FOREIGN — while the comment claimed the label was dropped. Not knowing and
	// being different are different things, and flattening them is what produced
	// the defect (CRI diff-gate P2-5).
	// The EFFECTIVE scope, so a legacy manifest of my own repository is not
	// presented as foreign: reading the manifest tells me it exists, deriving
	// tells me where it is, and only the second answers this question. Setting
	// known=true for any readable manifest made a sender from MY OWN project
	// arrive labelled cross-project (CRI2, the fourth face).
	myScope := ""
	if mf, lerr := mgr.LoadManifest(sid); lerr == nil {
		myScope = session.EffectiveScope(mf)
	}

	entries, corrupt, foreign, err := readMailbox(inboxDir, cfg.MaxMessageBytes)
	if err != nil {
		return pageResult{}, false, fmt.Errorf("next: read mailbox: %w", err)
	}
	// The delivery path refuses: handing over a page from a mailbox holding a
	// file we cannot vouch for is the one outcome worth stopping for.
	if len(foreign) > 0 {
		return pageResult{}, false, fmt.Errorf("next: refusing to deliver from this inbox: %w (%s)",
			security.ErrOwnershipMismatch, strings.Join(foreign, ", "))
	}

	present := make(map[string]bool, len(entries))
	var unread []mailboxEntry
	for _, e := range entries {
		present[e.msg.ID] = true
		if !cursor.IsNotified(e.msg.ID) {
			e.replayed = cursor.WasReplayed(e.msg.ID)
			unread = append(unread, e)
		}
	}
	if len(unread) == 0 {
		if len(corrupt) == 0 {
			return pageResult{}, false, nil
		}
		// An inbox holding ONLY unreadable files is not an idle inbox: staying
		// asleep for 24h would hide a broken mailbox behind a normal timeout.
		// Report it with zero messages and mark nothing.
		return pageResult{
			page: nextPayload{
				Status: nextStatusEmitted, Session: sid, Generation: generation,
				CorruptCount: len(corrupt), Corrupt: corrupt, Messages: []nextMessage{},
				Warnings: []string{fmt.Sprintf("%d unreadable file(s) in inbox and nothing else — no action needed from you; they are left in place", len(corrupt))},
				Hint:     "nothing readable arrived; run next again",
			},
			present: present,
		}, true, nil
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
		Status:       nextStatusEmitted,
		Session:      sid,
		Generation:   generation,
		Total:        len(unread),
		CorruptCount: len(corrupt),
		Messages:     []nextMessage{},
	}
	page.Corrupt = append(page.Corrupt, corrupt...)
	if cursorWarn != "" {
		page.Warnings = append(page.Warnings, cursorWarn)
	}
	if len(corrupt) > 0 {
		// Declared, never silently skipped (§2.7): a corrupt file must neither
		// block next forever nor vanish without a trace.
		page.Warnings = append(page.Warnings, fmt.Sprintf("%d unreadable file(s) left in inbox — no action needed from you", len(corrupt)))
	}

	var (
		emitted []string
		used    int
	)
	for _, e := range unread {
		if len(page.Messages) >= maxCount {
			break
		}
		// Budget the SERIALIZED size, not the file size: JSON escaping,
		// duplicated metadata and the wrapper can inflate a payload well past
		// the raw bytes on disk, and this limit exists to protect stdout, the
		// harness capture and the agent's context (CRI diff-gate P1-4).
		candidate := newNextMessage(e, false, myScope)
		size := serializedSize(candidate)

		// A single message over budget goes out alone as a pointer rather than
		// starving forever behind a limit it can never fit under.
		if size > maxBytes {
			if len(page.Messages) > 0 {
				break
			}
			page.Messages = append(page.Messages, newNextMessage(e, true, myScope))
			emitted = append(emitted, e.msg.ID)
			break
		}
		if used+size > maxBytes && len(page.Messages) > 0 {
			break
		}
		page.Messages = append(page.Messages, candidate)
		emitted = append(emitted, e.msg.ID)
		used += size
	}

	// Computed only on the emitting pass, never per poll: it costs one outbox
	// scan plus one index per distinct recipient.
	page.Outbound = collectOutboundAsks(mgr, cfg, sid)
	if open, oerr := collectOpenAsks(mgr, cfg, sid); oerr == nil {
		// Counted AFTER this page, not before: the asks being emitted right now
		// become open the moment the agent reads them, and the number it needs
		// is the one describing the situation it is about to be in — that is the
		// context `reply` works against.
		page.OpenAsks = len(open)
		for _, m := range page.Messages {
			if m.Type == message.TypeQuery {
				page.OpenAsks++
			}
		}
	}

	page.Returned = len(page.Messages)
	page.HasMore = page.Returned < page.Total
	// The output declares its own next action, so the agent never looks for a
	// --page flag that does not exist (§2.7).
	if page.HasMore {
		page.Hint = "hasMore: true — run next again for the rest"
	} else {
		page.Hint = "run next again to stay reachable"
	}

	return pageResult{page: page, emittedIDs: emitted, present: present}, true, nil
}

// collectOutboundAsks lists MY asks that are still waiting for an answer, with
// the state seen from the recipient's side.
//
// Best-effort by design: this is a summary line, so a mailbox that cannot be
// read costs its rows, never the delivery that the agent is actually waiting
// for.
func collectOutboundAsks(mgr *session.Manager, cfg config.Config, sid string) []outboundAsk {
	sentRows, err := collectSent(filepath.Join(cfg.DataDir, "sessions", sid, "outbox"), cfg.MaxMessageBytes)
	if err != nil {
		return nil
	}
	var asks []sentSummary
	for _, r := range sentRows {
		if r.Type == message.TypeQuery {
			asks = append(asks, r)
		}
	}
	if len(asks) == 0 {
		return nil
	}
	if err := annotateSentStates(cfg, mgr, asks); err != nil {
		return nil
	}

	var out []outboundAsk
	for _, r := range asks {
		// Still open = it reached them and they have not replied. `archived`
		// means their reply closed it; the rest are not "waiting on them".
		if r.State != sentStateUnread && r.State != sentStateNotified {
			continue
		}
		age := ""
		if t, terr := time.Parse(time.RFC3339, r.Timestamp); terr == nil {
			age = time.Since(t).Round(time.Minute).String()
		}
		name := r.To
		if mf, merr := mgr.LoadManifest(r.To); merr == nil && mf.AgentName != "" {
			name = mf.AgentName
		}
		out = append(out, outboundAsk{MsgID: r.MsgID, To: name, Age: age, State: r.State})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MsgID < out[j].MsgID })
	return out
}

// serializedSize is how many bytes this message will occupy in the emitted
// payload. Marshal failure falls back to the content length, which only ever
// under-counts a message we could not have emitted anyway.
func serializedSize(m nextMessage) int {
	data, err := json.Marshal(m)
	if err != nil {
		return len(m.Content)
	}
	return len(data)
}

func newNextMessage(e mailboxEntry, oversize bool, myScope string) nextMessage {
	m := nextMessage{
		ID:            e.msg.ID,
		From:          e.msg.From,
		FromAgentName: e.msg.FromAgentName,
		FromRole:      e.msg.FromRole,
		Type:          e.msg.Type,
		Timestamp:     e.msg.Timestamp,
		Bytes:         e.bytes,
		Closes:        e.msg.Closes,
	}
	// Only when it differs, and only when the sender stated it: an empty field
	// means "not stated" and must not be rendered as agreement.
	// crossesScopes, not "!=": an unknown scope on either side is not a crossing,
	// and the label must be dropped rather than guessed.
	if from := e.msg.Metadata.FromScope; session.CrossesScopes(from, myScope) {
		m.FromScope = from
		if e.msg.FromAgentName != "" {
			// ONE datum, two renderings, assigned together — no caller can
			// populate one without the other, so there is nothing to keep in
			// sync. Same presence conditions as FromAddress by construction.
			addr := recipient{name: e.msg.FromAgentName, scope: from}.String()
			m.FromAddress = addr
			m.FromAddressShellArg = shellarg.Quote(addr)
		}
	}
	if e.replayed {
		m.Redelivered = true
		m.Note = "re-delivered: you have been shown this before — treat it normally"
	}
	if oversize {
		m.Oversize = true
		m.BodyFile = e.path
		if m.Note != "" {
			m.Note += " · "
		}
		m.Note += "body too large to inline — read the file at bodyFile to get it (you can read it in parts)"
		return m
	}
	m.Content = e.msg.Content
	return m
}

// readMailbox decodes every message still in inbox/ and reports the files it
// could not decode instead of skipping them silently (collectInbox does skip —
// inbox.go:131-143 — which is exactly the behaviour §2.7 rules out).
// readMailbox returns the decoded entries, the unreadable ones, and — kept
// SEPARATE — the ones that do not belong to this user.
//
// Three buckets and not two, because the second version of this function decided
// the policy itself (hard-fail on a foreign file) and the decision then depended
// on which helper a command happened to call rather than on what the command
// does. `status.countUnread` swallowed that hard error and returned 0, turning a
// security anomaly back into "empty inbox" — the very defect it had just fixed,
// one caller downstream. A scanner reports; the command decides.
func readMailbox(inboxDir string, maxContentBytes int) ([]mailboxEntry, []string, []string, error) {
	dir, err := os.ReadDir(inboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}

	var (
		entries []mailboxEntry
		corrupt []string
		foreign []string
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
		data, err := security.ReadOwnedFile(full)
		if err != nil {
			// An ownership violation is NOT a corrupt file: it goes in its own
			// bucket so the caller can treat it as what it is.
			if errors.Is(err, security.ErrOwnershipMismatch) {
				foreign = append(foreign, name)
				continue
			}
			corrupt = append(corrupt, name)
			continue
		}
		m, err := message.DecodeLenient(data, maxContentBytes)
		if err != nil {
			corrupt = append(corrupt, name)
			continue
		}
		// Legacy delivery receipts must never wake anyone. The ack type is on
		// its way out (§2.4) but auto-ack still runs while `listen` exists, so
		// without this filter the command built to kill S2 would be woken by
		// exactly the noise S2 is about (CRI diff-gate, integration blocker).
		// Kept as a read-side filter, not a write-side assumption: acks already
		// on disk stay readable.
		if m.Type == message.TypeAck {
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
	sort.Strings(foreign)
	return entries, corrupt, foreign, nil
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
