package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// --- helpers ---------------------------------------------------------------

func nextTestConfig(dataDir string) config.Config {
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.PollIntervalMs = 10
	cfg.HeartbeatTickMs = 50
	return cfg
}

// runNextOnce drives nextRun with a short parent deadline so an idle wait ends
// in milliseconds instead of the configured 24h window, and decodes the JSONL
// stream: a page record, then (when something was emitted) a commit record.
func runNextOnce(t *testing.T, mgr *session.Manager, cfg config.Config, sid string, wait time.Duration) nextPayload {
	t.Helper()
	page, _ := runNextRecords(t, mgr, cfg, sid, wait, true)
	return page
}

func runNextRecords(t *testing.T, mgr *session.Manager, cfg config.Config, sid string, wait time.Duration, wantOK bool) (nextPayload, nextCommitRecord) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := nextRun(ctx, mgr, cfg, sid, &stdout, &stderr)
	if wantOK {
		require.NoError(t, err)
	}

	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var page nextPayload
	var commit nextCommitRecord
	require.NoError(t, dec.Decode(&page), "first record must be the page: %s", stdout.String())
	if dec.More() {
		require.NoError(t, dec.Decode(&commit), "second record must be the commit: %s", stdout.String())
	}
	return page, commit
}

func inboxFiles(t *testing.T, dataDir, sid string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions", sid, "inbox"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func messageIDs(p nextPayload) []string {
	var ids []string
	for _, m := range p.Messages {
		ids = append(ids, m.ID)
	}
	return ids
}

// newNextSession creates a registered session plus a manager pointed at it.
func newNextSession(t *testing.T) (*session.Manager, config.Config, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := nextTestConfig(dataDir)
	const sid = "nxtses01"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-next", "/repo/next", "", "working")
	return newSessionManager(cfg), cfg, sid, dataDir
}

// --- the invariant (§2.3) ---------------------------------------------------

// TestNext_Invariant_MarksOnlyWhatItEmits is the dedicated test the brief asks
// for: only what is emitted becomes NOTIFIED, and everything UNREAD is emitted
// within the declared limits. It is checked in both regimes — a page that fits
// and a page that is truncated by the limits.
func TestNext_Invariant_MarksOnlyWhatItEmits(t *testing.T) {
	t.Run("everything_fits", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		base := time.Now().UTC()
		for i, id := range []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc"} {
			plantInboxAt(t, dataDir, sid, id, "valxxx01", message.TypeQuery, "body", base.Add(time.Duration(i)*time.Second))
		}

		p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
		require.Equal(t, nextStatusEmitted, p.Status)
		assert.Equal(t, 3, p.Total)
		assert.Equal(t, 3, p.Returned)
		assert.False(t, p.HasMore)

		cursor, warn, err := mgr.ReadWakeCursor(sid)
		require.NoError(t, err)
		assert.Empty(t, warn)
		assert.Len(t, cursor.Notified, 3, "exactly the emitted ids are NOTIFIED")
		for _, id := range messageIDs(p) {
			assert.True(t, cursor.IsNotified(id), "%s was emitted so it must be NOTIFIED", id)
		}
	})

	t.Run("truncated_by_limits_marks_only_the_emitted_page", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		cfg.MaxPageMessages = 2
		base := time.Now().UTC()
		ids := []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc", "msg-dddddddddddd"}
		for i, id := range ids {
			plantInboxAt(t, dataDir, sid, id, "valxxx01", message.TypeQuery, "body", base.Add(time.Duration(i)*time.Second))
		}

		p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
		assert.Equal(t, 4, p.Total, "total declares everything UNREAD")
		assert.Equal(t, 2, p.Returned)
		assert.True(t, p.HasMore)
		assert.Contains(t, p.Hint, "next", "the payload declares its own follow-up action")

		cursor, _, err := mgr.ReadWakeCursor(sid)
		require.NoError(t, err)
		assert.Len(t, cursor.Notified, 2, "the two left behind must NOT be marked")
		assert.True(t, cursor.IsNotified(ids[0]))
		assert.True(t, cursor.IsNotified(ids[1]))
		assert.False(t, cursor.IsNotified(ids[2]), "un-emitted message must stay UNREAD")
		assert.False(t, cursor.IsNotified(ids[3]))

		// The remainder arrives with the next run, without waiting the window.
		p2 := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
		assert.Equal(t, []string{ids[2], ids[3]}, messageIDs(p2))
		assert.False(t, p2.HasMore)
	})
}

// TestNext_NeverMovesAFile pins the contract line that makes two conflicting
// implementations impossible: next is pure-read.
func TestNext_NeverMovesAFile(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "body", time.Now().UTC())
	before := inboxFiles(t, dataDir, sid)

	runNextOnce(t, mgr, cfg, sid, 2*time.Second)

	assert.Equal(t, before, inboxFiles(t, dataDir, sid), "inbox must be untouched")
	processed := filepath.Join(dataDir, "sessions", sid, "processed")
	_, err := os.Stat(processed)
	assert.True(t, os.IsNotExist(err), "next must not even create processed/")

	// And a second run does not re-deliver what is already NOTIFIED: it keeps
	// waiting and reports only the interruption, never a page.
	assert.NotContains(t, runNextUntilCancel(t, mgr, cfg, sid, 200*time.Millisecond), nextStatusEmitted,
		"nothing may be delivered when there is nothing new")
}

// --- crash between print and commit ----------------------------------------

// TestNext_CrashBetweenPrintAndCommit_RedeliversNeverLoses reproduces the crash
// the obligatory ordering is designed for: the page was printed but the cursor
// never committed. The messages must come back (at-least-once), not disappear.
func TestNext_CrashBetweenPrintAndCommit_RedeliversNeverLoses(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "brief", time.Now().UTC())

	// Emit without committing — exactly the state a crash in the gap leaves.
	inboxDir := filepath.Join(dataDir, "sessions", sid, "inbox")
	res, ready, err := collectNextPage(mgr, cfg, sid, inboxDir, 1)
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, []string{"msg-aaaaaaaaaaaa"}, res.emittedIDs)

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	require.Empty(t, cursor.Notified, "nothing committed yet")

	// The message is delivered AGAIN rather than lost.
	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(p))
}

// --- concurrency ------------------------------------------------------------

// TestNext_ConcurrentRuns_DoNotOverlap covers the third test the brief asks
// for. Wait ownership is single: the second claim revokes the first, and the
// evicted instance cannot commit a delivery for the session it no longer owns.
func TestNext_ConcurrentRuns_DoNotOverlap(t *testing.T) {
	t.Run("evicted_owner_cannot_commit", func(t *testing.T) {
		mgr, _, sid, _ := newNextSession(t)

		first, err := mgr.ClaimListener(sid)
		require.NoError(t, err)
		second, err := mgr.ClaimListener(sid)
		require.NoError(t, err)
		require.NotEqual(t, first.Token, second.Token)
		assert.Greater(t, second.Generation, first.Generation)

		firstOK := func() bool { return mgr.IsListenerCurrent(sid, first.Token) }
		evicted, err := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa"}, time.Now().UTC(), firstOK, nil)
		require.NoError(t, err)
		assert.True(t, evicted, "the superseded instance must be told it lost ownership")

		cursor, _, err := mgr.ReadWakeCursor(sid)
		require.NoError(t, err)
		assert.Empty(t, cursor.Notified, "an evicted instance must not mark anything NOTIFIED")

		secondOK := func() bool { return mgr.IsListenerCurrent(sid, second.Token) }
		evicted, err = mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa"}, time.Now().UTC(), secondOK, nil)
		require.NoError(t, err)
		assert.False(t, evicted)
		cursor, _, err = mgr.ReadWakeCursor(sid)
		require.NoError(t, err)
		assert.True(t, cursor.IsNotified("msg-aaaaaaaaaaaa"), "the current owner commits normally")
	})

	// The end-to-end half of this guarantee — "a message wakes exactly one
	// instance" — used to live here as two goroutines running nextRun. It cannot
	// be established that way, and it moved to
	// tests/integration/scenario_two_waiters_test.go, where two real PROCESSES run
	// the real binary.
	//
	// The reason is not tidiness. AcquireLock is deliberately RE-ENTRANT for the
	// same PID (internal/session/lock.go:57-62), so two goroutines of one test
	// binary both enter the critical section that is supposed to keep them apart.
	// The in-process version therefore asserted an exclusion its own setup
	// dissolved, and passed on timing alone: removing two manifest writes from the
	// wait path — writes that had nothing to do with delivery — turned it red 8
	// times out of 8, while the same scenario with real processes stayed green,
	// because there the second `next` is genuinely locked out.
	//
	// A green test resting on a premise its setup contradicts is worse than no
	// test: it reports a guarantee nobody checked.
}

// --- paging, ordering, oversize --------------------------------------------

func TestNext_OrdersByDecodedTimestampWithIDTieBreak(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	// Lexically the ids sort z, m, a — arrival order is the opposite, and two
	// share a timestamp so the id tie-break decides.
	plantInboxAt(t, dataDir, sid, "msg-zzzzzzzzzzzz", "valxxx01", message.TypeQuery, "first", base)
	plantInboxAt(t, dataDir, sid, "msg-mmmmmmmmmmmm", "valxxx01", message.TypeQuery, "second", base.Add(time.Minute))
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "third", base.Add(time.Minute))

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	assert.Equal(t, []string{"msg-zzzzzzzzzzzz", "msg-aaaaaaaaaaaa", "msg-mmmmmmmmmmmm"}, messageIDs(p),
		"oldest first; equal timestamps break by id")
}

func TestNext_PagingRespectsBothLimits(t *testing.T) {
	t.Run("byte_budget_truncates_before_the_count", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		cfg.MaxPageMessages = 100
		cfg.MaxPageBytes = 2000
		base := time.Now().UTC()
		body := strings.Repeat("x", 900)
		for i, id := range []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc", "msg-dddddddddddd"} {
			plantInboxAt(t, dataDir, sid, id, "valxxx01", message.TypeQuery, body, base.Add(time.Duration(i)*time.Second))
		}

		p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
		assert.Equal(t, 4, p.Total)
		assert.Less(t, p.Returned, 4, "the byte budget must bite before the count limit")
		assert.True(t, p.HasMore)
	})

	t.Run("single_oversize_message_goes_out_alone_as_a_pointer", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		cfg.MaxPageBytes = 500
		base := time.Now().UTC()
		plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, strings.Repeat("y", 4000), base)
		plantInboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "valxxx01", message.TypeQuery, "small", base.Add(time.Second))

		p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
		require.Len(t, p.Messages, 1, "an oversize message must never starve, and never drag others along")
		got := p.Messages[0]
		assert.True(t, got.Oversize)
		assert.Empty(t, got.Content, "the body is not inlined")
		assert.NotEmpty(t, got.BodyFile, "the on-disk path is emitted instead")
		assert.FileExists(t, got.BodyFile)
		assert.True(t, p.HasMore)
	})
}

// --- corrupt files and cursor recovery -------------------------------------

func TestNext_CorruptFilesAreDeclaredNotSkipped(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "good", time.Now().UTC())
	inboxDir := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.WriteFile(filepath.Join(inboxDir, "msg-broken0000.json"), []byte("{not json"), 0o600))

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	assert.Equal(t, 1, p.CorruptCount, "a corrupt file is counted, never silently skipped")
	assert.Contains(t, p.Corrupt, "msg-broken0000.json")
	assert.NotEmpty(t, p.Warnings)
	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(p), "the good message still gets through")
	assert.FileExists(t, filepath.Join(inboxDir, "msg-broken0000.json"), "the corrupt file stays for inspection")
}

func TestNext_CursorRecoveryIsAtLeastOnce(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"corrupt_json", "{{{not a cursor"},
		{"truncated", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, cfg, sid, dataDir := newNextSession(t)
			plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "brief", time.Now().UTC())

			// A first delivery marks it NOTIFIED...
			p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
			require.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(p))

			// ...then the cursor is damaged.
			cursorPath := filepath.Join(dataDir, "sessions", sid, "wake-cursor.json")
			require.NoError(t, os.WriteFile(cursorPath, []byte(tc.content), 0o600))

			p2 := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
			assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(p2),
				"a damaged cursor replays; it must never be read as already-notified")
			assert.NotEmpty(t, p2.Warnings, "the replay is announced, not silent")
		})
	}
}

func TestNext_CursorIsPrunedToWhatIsStillInTheMailbox(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "one", time.Now().UTC())
	runNextOnce(t, mgr, cfg, sid, 2*time.Second)

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	require.True(t, cursor.IsNotified("msg-aaaaaaaaaaaa"))

	// Something else archived it (phase 1a still coexists with listen, and 1b's
	// reply will do the same): the stale id must not accumulate forever.
	require.NoError(t, os.Remove(filepath.Join(dataDir, "sessions", sid, "inbox", "msg-aaaaaaaaaaaa.json")))
	plantInboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "valxxx01", message.TypeQuery, "two", time.Now().UTC())

	runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	cursor, _, err = mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.False(t, cursor.IsNotified("msg-aaaaaaaaaaaa"), "id no longer in the mailbox is pruned")
	assert.True(t, cursor.IsNotified("msg-bbbbbbbbbbbb"))
}

// --- envelope and surface ---------------------------------------------------

// TestNext_ReturnsImmediatelyAfterDelivery is the F-94 guard, and it is a
// timing test on purpose.
//
// The design requires that next return as soon as it has something, so a
// delivered message never sits in the buffer of a process that stays alive.
// A version of this command that waits out its window before returning still
// passes every functional assertion — it just holds the message for up to 24h,

// TestNext_ReturnsOnlyOnDeliverySignalOrReclaim_NeverOnTime is the contract of
// the windowless wait (§2.2 rev. cdb21dc).
//
// A window that expires is the waiter dismissing itself, and only Alan closes a
// session. So `next` must never come back for having waited long enough: the
// only exits are a delivery, a signal, or another instance taking over.
func TestNext_ReturnsOnlyOnDeliverySignalOrReclaim_NeverOnTime(t *testing.T) {
	t.Run("waits_indefinitely_on_an_empty_mailbox", func(t *testing.T) {
		mgr, cfg, sid, _ := newNextSession(t)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		var stdout, stderr bytes.Buffer
		go func() { done <- nextRun(ctx, mgr, cfg, sid, &stdout, &stderr) }()

		select {
		case err := <-done:
			t.Fatalf("next returned on its own after no time at all (err=%v, out=%q) — it must not dismiss itself", err, stdout.String())
		case <-time.After(400 * time.Millisecond):
		}

		// Only an explicit cancel ends it, and it emits nothing.
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
			assert.NotContains(t, stdout.String(), nextStatusEmitted, "an interrupted wait delivers nothing")
			assert.Contains(t, stdout.String(), nextStatusInterrupted, "but it says that it was interrupted")
		case <-time.After(3 * time.Second):
			t.Fatal("next did not return after cancel — the teardown must not depend on a deadline")
		}
	})

	t.Run("returns_as_soon_as_something_arrives", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		var stdout, stderr bytes.Buffer
		go func() { done <- nextRun(ctx, mgr, cfg, sid, &stdout, &stderr) }()

		time.Sleep(100 * time.Millisecond)
		plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "arrived late", time.Now().UTC())

		select {
		case err := <-done:
			require.NoError(t, err)
			assert.Contains(t, stdout.String(), "msg-aaaaaaaaaaaa")
		case <-time.After(5 * time.Second):
			t.Fatal("next did not wake on delivery")
		}
	})
}

// runNextUntilCancel runs next with a parent that is cancelled after d, and

// TestNext_AdoptsPIDWhileWaiting is the F-95 half that survives the windowless
// contract: a waiting session must be ALIVE, not look stale.
//
// The deadline half is gone with the window — there is nothing left to publish,
// so `listening` now rests on the live PID, which was the load-bearing fact all
// along.
func TestNext_AdoptsPIDWhileWaiting(t *testing.T) {
	mgr, cfg, sid, _ := newNextSession(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	go func() { done <- nextRun(ctx, mgr, cfg, sid, &stdout, &stderr) }()

	time.Sleep(150 * time.Millisecond)
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), mf.PID, "F-95: a waiting next owns the PID and does not look stale")

	cancel()
	<-done
}

func TestRunNext_RejectsAnyArgument(t *testing.T) {
	// §2.2: no flags at all. Accepting one silently would reintroduce exactly
	// the per-cycle decision the design removes.
	for _, arg := range []string{"--session-id=abc123", "--until-deadline=2h", "extra"} {
		err := runNext([]string{arg})
		require.Error(t, err, "next must reject %q", arg)
		assert.Contains(t, err.Error(), "no arguments")
	}
}

func TestParseMessageTime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		zero bool
	}{
		{"rfc3339", "2026-08-08T12:00:00Z", false},
		{"rfc3339_nano", "2026-08-08T12:00:00.123456789Z", false},
		{"offset", "2026-08-08T14:00:00+02:00", false},
		{"garbage", "not-a-time", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMessageTime(tc.in)
			assert.Equal(t, tc.zero, got.IsZero())
		})
	}
}

// --- CRI diff-gate fixes ----------------------------------------------------

// TestNext_LegacyAcksNeverWake is the integration blocker: auto-ack still runs
// while `listen` exists, so without a read-side filter the command built to
// kill S2 would be woken by exactly the receipts S2 is about.
func TestNext_LegacyAcksNeverWake(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	base := time.Now().UTC()
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeAck, "ACK msg-x: received", base)
	plantInboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "valxxx01", message.TypeAck, "ACK msg-y: received", base)

	// An inbox of nothing but acks must look idle: next keeps waiting and never
	// emits a page.
	assert.NotContains(t, runNextUntilCancel(t, mgr, cfg, sid, 300*time.Millisecond), nextStatusEmitted,
		"acks must not wake next")

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.Empty(t, cursor.Notified, "an ack is never marked NOTIFIED")

	// A real message still gets through, and the acks stay out of the page.
	plantInboxAt(t, dataDir, sid, "msg-cccccccccccc", "valxxx01", message.TypeQuery, "the real brief", base.Add(time.Second))
	p2 := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	assert.Equal(t, []string{"msg-cccccccccccc"}, messageIDs(p2), "only the real message is delivered")
	assert.Equal(t, 1, p2.Total)
}

// TestNext_InboxOfOnlyCorruptFilesIsReported: a broken mailbox must not hide
// behind a 24h idle timeout (CRI P1-3).
func TestNext_InboxOfOnlyCorruptFilesIsReported(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	inboxDir := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.MkdirAll(inboxDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(inboxDir, "msg-broken0000.json"), []byte("{nope"), 0o600))

	p := runNextOnce(t, mgr, cfg, sid, 5*time.Second)
	assert.Equal(t, nextStatusEmitted, p.Status, "must report rather than sleep out the window")
	assert.Equal(t, 1, p.CorruptCount)
	assert.Empty(t, p.Messages)
	assert.NotEmpty(t, p.Warnings)

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.Empty(t, cursor.Notified, "nothing was emitted, so nothing may be marked")
}

// TestNext_PageRecordNeverClaimsAnOutcome: with emit-before-commit no single
// record can know its own future, so the page states a fact ("emitted") and the
// outcome arrives in its own record (CRI P0-2).
func TestNext_PageRecordNeverClaimsAnOutcome(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "brief", time.Now().UTC())

	page, commit := runNextRecords(t, mgr, cfg, sid, 2*time.Second, true)
	assert.Equal(t, nextStatusEmitted, page.Status)
	assert.NotEqual(t, "delivered", page.Status, "a page must never certify delivery")
	assert.Equal(t, nextStatusCommitted, commit.Status, "the outcome comes in the commit record")
	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, commit.Confirmed)
}

// TestNext_NotCommittedRecordIsActionable checks the wording the agent acts on
// (CRI2 P1-a/P1-b): the record must contradict the page above it, forbid the
// re-run whose instinct would steal the wait from the instance that replaced
// this one, and avoid internal jargon.
//
// The end-to-end race (eviction landing between emit and commit) is NOT covered
// deterministically: nextRun claims ownership at startup, so an external claim
// either precedes it (and is superseded) or trips the reclaim watcher, which
// cancels the context and ends the run as a normal timeout. The fencing itself
// is covered by TestNext_ConcurrentRuns_DoNotOverlap; what is asserted here is
// the contract of the record and that such a run cannot exit 0.
func TestNext_NotCommittedRecordIsActionable(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "brief", time.Now().UTC())

	// Emit a page, then have "another instance" take over before the commit.
	inboxDir := filepath.Join(dataDir, "sessions", sid, "inbox")
	mine, err := mgr.StartWait(sid)
	require.NoError(t, err)
	res, ready, err := collectNextPage(mgr, cfg, sid, inboxDir, mine.Generation)
	require.NoError(t, err)
	require.True(t, ready)

	_, err = mgr.ClaimListener(sid) // the takeover
	require.NoError(t, err)

	evicted, err := mgr.CommitWakeCursor(sid, res.emittedIDs, time.Now().UTC(),
		func() bool { return mgr.IsListenerCurrent(sid, mine.Token) }, res.present)
	require.NoError(t, err)
	require.True(t, evicted, "the superseded run must be refused")

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.Empty(t, cursor.Notified, "an evicted run marks nothing")

	// The wording an agent acts on — the constants the code actually emits.
	//
	// The two takeover hints must FORBID the re-run, because another instance is
	// waiting and retrying would steal the wait from it. The commit-failure hint
	// must NOT forbid it: there is no other instance, the messages simply stayed
	// unread, and running next again is exactly the right move. Same "not
	// committed" status, opposite instructions — collapsing them would teach the
	// agent the wrong reflex in one of the two.
	for name, hint := range map[string]string{
		"takeover_before_emit": hintTakeoverBeforeEmit,
		"takeover_after_emit":  hintTakeoverAfterEmit,
	} {
		lower := strings.ToLower(hint)
		assert.Contains(t, lower, "this session", "%s: name the situation in the agent's terms", name)
		assert.NotContains(t, lower, "ownership", "%s: no internal jargon — the agent has never been shown wait ownership", name)
		assert.Contains(t, lower, "do not run next again", "%s: retrying is the instinct, and it steals the wait", name)
	}
	assert.NotContains(t, strings.ToLower(hintCommitFailed), "do not run next again",
		"an I/O failure has no rival instance: re-running is the correct recovery")

	// Both post-emission hints must contradict the page the agent just read.
	assert.Contains(t, hintTakeoverAfterEmit, "IGNORE the emitted page")
	assert.Contains(t, hintCommitFailed, "IGNORE the emitted page")
}

// TestStartWait_ResumeCannotBeDefeatedByTheWaiterItEvicted is the P0-1
// regression: adopt and claim must be one locked operation, or the waiter a
// resume just evicted can re-authorise itself on its way in.
func TestStartWait_ResumeCannotBeDefeatedByTheWaiterItEvicted(t *testing.T) {
	mgr, _, sid, _ := newNextSession(t)

	// An old waiter starts up.
	old, err := mgr.StartWait(sid)
	require.NoError(t, err)

	// A resume elsewhere revokes it.
	info, err := mgr.ReclaimListener(sid)
	require.NoError(t, err)
	assert.Greater(t, info.NewGeneration, old.Generation)

	// The evicted waiter must NOT be able to consider itself current again.
	assert.False(t, mgr.IsListenerCurrent(sid, old.Token), "revocation must be monotonic")

	// And a fresh startup supersedes the reclaim rather than resurrecting the old token.
	fresh, err := mgr.StartWait(sid)
	require.NoError(t, err)
	assert.Greater(t, fresh.Generation, info.NewGeneration)
	assert.False(t, mgr.IsListenerCurrent(sid, old.Token))
	assert.True(t, mgr.IsListenerCurrent(sid, fresh.Token))
}

// runNextUntilCancel runs next with a parent cancelled after d, and returns
// whatever it emitted — normally nothing, since a windowless wait has no empty
// result to report.
func runNextUntilCancel(t *testing.T, mgr *session.Manager, cfg config.Config, sid string, d time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var stdout, stderr bytes.Buffer
	require.NoError(t, nextRun(ctx, mgr, cfg, sid, &stdout, &stderr))
	return stdout.String()
}

// TestNext_LeavesNoWaitMarkerBehind pins the invariant that repaired the
// self-inflicted eviction cycle: a `next` on its way out writes NOTHING about
// waiting.
//
// It used to clear a manifest marker from a defer, through a setter that held
// only an in-process mutex and never checked ownership — so an EVICTED instance
// erased the marker of the live one that had replaced it, and overview reported
// a listening session as idle. Asserting on the raw manifest bytes rather than
// on a struct field is deliberate: the field is gone, and a future one added
// back on this path would be caught here rather than at midnight.
//
// The ownership record is the other half: it must still name the owner after the
// waiter returns. Nothing revoked it, so nothing may erase it.
func TestNext_LeavesNoWaitMarkerBehind(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)

	runNextUntilCancel(t, mgr, cfg, sid, 200*time.Millisecond)

	raw, err := os.ReadFile(filepath.Join(dataDir, "sessions", sid, "manifest.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "waitingSince", "a departing waiter publishes no marker to clear")
	assert.NotContains(t, string(raw), "listenUntil", "and no deadline: a wait has none")

	owner, ok, err := mgr.ReadListener(sid)
	require.NoError(t, err)
	require.True(t, ok, "the claim survives the waiter that made it")
	assert.Equal(t, os.Getpid(), owner.PID, "exiting is not revoking — only a reclaim revokes")
}

// TestNext_DeliversAMessageArrivingMidWait restores a guarantee that evaporated
// with the files (CRI 1c matrix): the removed TestPollInbox_EmitsMessagesArriving
// MidLoop, TestReceiveAny_BatchArrivingMidWait and TestBUG2_LateReplyNotLost all
// covered "a message that arrives AFTER the wait started", and nothing in
// next_test exercised it.
//
// With the window now infinite that case is not an edge — it is THE normal one:
// a waiter that only ever saw pre-existing mail would be useless.
func TestNext_DeliversAMessageArrivingMidWait(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	go func() { done <- nextRun(ctx, mgr, cfg, sid, &stdout, &stderr) }()

	// Let the wait settle on an EMPTY mailbox first, so the message genuinely
	// arrives mid-wait rather than being there from the start.
	time.Sleep(150 * time.Millisecond)
	require.Empty(t, stdout.String(), "nothing to deliver yet")

	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "arrived mid-wait", time.Now().UTC())

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("a message arriving mid-wait was never delivered")
	}

	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var page nextPayload
	require.NoError(t, dec.Decode(&page))
	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(page))

	var commit nextCommitRecord
	require.NoError(t, dec.Decode(&commit))
	assert.Equal(t, nextStatusCommitted, commit.Status)
}

// TestNext_MarksARedeliveryInline is the §2.3 marker (CRI2 P1-3): after a join
// replay, the message must SAY it is a re-delivery where the agent reads it.
//
// The join line announcing the replay lives on another command's stderr, minutes
// earlier and without ids — exactly the correlation-at-a-distance the inline
// field exists to remove.
func TestNext_MarksARedeliveryInline(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "the brief", time.Now().UTC())

	first := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	require.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(first))
	assert.False(t, first.Messages[0].Redelivered, "a first delivery is not a re-delivery")

	// A join replays it (the crash-then-resume path).
	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-aaaaaaaaaaaa"}))

	second := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	require.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(second))
	assert.True(t, second.Messages[0].Redelivered, "the replay must be visible ON the message")
	assert.Contains(t, second.Messages[0].Note, "re-delivered")
	assert.Contains(t, second.Messages[0].Note, "treat it normally", "and say what to do about it: nothing")
	assert.NotContains(t, second.Messages[0].Note, "restart",
		"a replay can come from a re-join in the SAME life: asserting a restart would be false in the benign case")

	// The marker is one-shot: a third delivery is not a re-delivery again.
	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-aaaaaaaaaaaa"}))
	_, err := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa"}, time.Now().UTC(), nil, nil)
	require.NoError(t, err)
	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.False(t, cursor.WasReplayed("msg-aaaaaaaaaaaa"), "the marker is cleared once re-delivered")
}

// TestNext_InterruptedWaitSaysSo: exiting 0 with zero bytes left a wrapper
// unable to tell "interrupted while waiting" from "nothing happened" (CRI2 P1-1).
func TestNext_InterruptedWaitSaysSo(t *testing.T) {
	mgr, cfg, sid, _ := newNextSession(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	require.NoError(t, nextRun(ctx, mgr, cfg, sid, &stdout, &stderr))

	var rec nextCommitRecord
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rec), "an interrupted wait must still report: %q", stdout.String())
	assert.Equal(t, nextStatusInterrupted, rec.Status)
	assert.Contains(t, rec.Hint, "run next again")
}

// TestNext_SummaryCarriesOutboundAndOpenAsks is the §2.2 stitch: with the ACKs
// gone, "did they get my brief?" would otherwise require REMEMBERING to run
// `sent` — i.e. spending the scarce resource that §2.2 itself rules out.
//
// It lives in next's payload, not in a command of its own, because next is run
// anyway: information at zero cost instead of one more thing to choose.
func TestNext_SummaryCarriesOutboundAndOpenAsks(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	const peer = "valsum01"
	plantOverviewSession(t, dataDir, peer, session.RoleVal, "VAL-sum", "/repo/next", "", session.StateOrchestrating)

	now := time.Now().UTC()
	// An ask I sent that is still sitting unread in THEIR inbox.
	plantInboxAt(t, dataDir, peer, "msg-out111111111"[:16], sid, message.TypeQuery, "my brief to them", now.Add(-30*time.Minute))
	plantOutboxAt(t, dataDir, sid, "msg-out111111111"[:16], peer, message.TypeQuery, "my brief to them", now.Add(-30*time.Minute))
	// A tell I sent: must NOT appear — fire and forget.
	plantOutboxAt(t, dataDir, sid, "msg-out222222222"[:16], peer, message.TypeNotify, "an update", now)
	// And an ask THEY sent me, waiting to be delivered.
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", peer, message.TypeQuery, "their brief", now)

	page := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	require.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(page))

	require.Len(t, page.Outbound, 1, "only asks count: a tell would grow the counter forever")
	assert.Equal(t, "VAL-sum", page.Outbound[0].To, "by name, not by opaque id")
	assert.Equal(t, sentStateUnread, page.Outbound[0].State, "they have not even been shown it yet")
	assert.NotEmpty(t, page.Outbound[0].Age)

	assert.Equal(t, 1, page.OpenAsks, "the ask just delivered is now open on my side")
}

// TestNext_InterruptedRecordCarriesOutbound is "and the branch next door?"
// applied to the summary itself.
//
// outbound was added to the EMITTING pass; its complement is the pass that
// emits nothing, and that is exactly where "did my brief arrive?" is asked
// hardest — the agent asked, waited, and got nothing back. openAsks is
// deliberately absent: no page was emitted, so that number has not changed.
func TestNext_InterruptedRecordCarriesOutbound(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	const peer = "valint01"
	plantOverviewSession(t, dataDir, peer, session.RoleVal, "VAL-int", "/repo/next", "", session.StateOrchestrating)

	now := time.Now().UTC()
	plantInboxAt(t, dataDir, peer, "msg-aaaaaaaaaaaa", sid, message.TypeQuery, "my brief", now.Add(-20*time.Minute))
	plantOutboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", peer, message.TypeQuery, "my brief", now.Add(-20*time.Minute))

	// An empty mailbox: the wait is interrupted having delivered nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	require.NoError(t, nextRun(ctx, mgr, cfg, sid, &stdout, &stderr))

	var rec nextCommitRecord
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rec))
	require.Equal(t, nextStatusInterrupted, rec.Status)
	require.Len(t, rec.Outbound, 1, "the one thing the agent cannot work out for itself must be here")
	assert.Equal(t, "VAL-int", rec.Outbound[0].To)
	assert.NotEmpty(t, rec.Outbound[0].Age)
	assert.NotNil(t, rec.Confirmed, "confirmed stays an array on every path")
}

// TestNext_EveryRecordCarriesTheAgentName closes the failure a val had three
// times in a row: re-arming onto the WRONG session without noticing, because
// the payload carried `session: 679b7060` and nothing else.
//
// An eight-hex id is something you RECOGNISE; a name is something you READ.
// The distinction is not cosmetic — it is the difference between a check the
// agent has to perform and one that performs itself.
func TestNext_EveryRecordCarriesTheAgentName(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)

	t.Run("on_the_page_and_on_the_commit", func(t *testing.T) {
		plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "brief", time.Now().UTC())
		page, commit := runNextRecords(t, mgr, cfg, sid, 2*time.Second, true)
		assert.Equal(t, "ESC-next", page.AgentName, "the page says who you are, not just which id")
		assert.Equal(t, "ESC-next", commit.AgentName, "and so does the outcome record")
	})

	t.Run("on_the_interrupted_record_too", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		var stdout, stderr bytes.Buffer
		require.NoError(t, nextRun(ctx, mgr, cfg, sid, &stdout, &stderr))

		var rec nextCommitRecord
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &rec))
		require.Equal(t, nextStatusInterrupted, rec.Status)
		assert.Equal(t, "ESC-next", rec.AgentName, "the silent path is where identity matters most")
	})
}

// The CRI design-gate's second half, end to end: a planted file that is not
// ours must not be reported to the agent as "an unreadable file, no action
// needed from you". That sentence is what the consume path used to print for the
// single security event this whole check exists to surface — every error from
// the read was flattened into "corrupt".
//
// A symlink stands in for the planted file because a test cannot create one
// owned by another uid, and it exercises the same refusal: the primitive treats
// "not a regular file of ours" as one verdict.
func TestNext_RefusesToDeliverFromAnInboxHoldingAPlantedFile(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "genuine", time.Now().UTC())

	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	target := filepath.Join(dataDir, "elsewhere.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"id":"msg-bbbbbbbbbbbb"}`), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json")))

	// The scanner REPORTS: three buckets, and the foreign file lands in its own
	// rather than being called corrupt or deciding the policy for every caller.
	entries, corrupt, foreign, err := readMailbox(inbox, cfg.MaxMessageBytes)
	require.NoError(t, err, "scanning is not where the refusal belongs")
	assert.Len(t, entries, 1, "the genuine message is still readable")
	assert.Empty(t, corrupt, "a file that is not ours is not a corrupt file")
	assert.Equal(t, []string{"msg-bbbbbbbbbbbb.json"}, foreign)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	rerr := nextRun(ctx, mgr, cfg, sid, &stdout, &stderr)
	require.Error(t, rerr, "and next itself fails rather than delivering a page from that inbox")
	assert.ErrorIs(t, rerr, security.ErrOwnershipMismatch, "the COMMAND applies the policy, and says which one")
	assert.NotContains(t, stdout.String(), "msg-aaaaaaaaaaaa",
		"no page is emitted at all: the mailbox is not trustworthy right now")
}

// --- F-124: the shell-safe address costs bytes, and bytes are budgeted -------

const crossSenderScope = "/repo/elsewhere"

// plantCrossScopeInbox plants a message from ANOTHER project, which is the only
// case that carries fromAddress — and therefore the only one that carries its
// shell-safe twin.
func plantCrossScopeInbox(t *testing.T, dataDir, sid, id string, ts time.Time) {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          "farsess1",
		FromRole:      "val",
		FromAgentName: "VAL-far",
		To:            sid,
		ToRole:        "esc",
		Type:          message.TypeQuery,
		Timestamp:     ts.UTC().Format(time.RFC3339Nano),
		Status:        message.StatusPending,
		Content:       "body",
		Metadata: message.Metadata{
			FromProject: "far", FromScope: crossSenderScope, ProcessingState: message.StatusPending,
		},
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600))
}

// budgetTestInstant is the timestamp every message in the page-budget test
// carries, measurement included, and both halves of that matter.
//
// FIXED NANOSECONDS, because RFC3339Nano DROPS TRAILING ZEROS: the serialized
// timestamp is 25, 26 or 27 characters long depending on what time.Now() happens
// to return (measured: 225/23/2 out of 250 calls on this machine). The budget
// below is exact to the byte, so one character of difference decides the test.
//
// THE SAME INSTANT for the measurement and for the planted messages, because the
// two used to call time.Now() separately: the cost was measured on one timestamp
// and applied to another. That was 25 failures in 250 runs — 10%, and exactly the
// 25 runs where the measured timestamp was the shorter one. Reported as "Go 1.27
// broke it" and it was neither Go nor 1.27: it failed on 1.26 too, at a different
// rate, which is what a coin looks like when you flip it once per version.
//
// Truncate+Add rather than a literal date: the test keeps meaning "now", it just
// stops meaning "now, to whatever precision the clock felt like".
func budgetTestInstant() time.Time {
	return time.Now().UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
}

// crossScopeMessageCost measures, on a REAL emitted page, what one cross-scope
// message costs with the shell field and what the same message cost before the
// field existed.
//
// at MUST be the same instant the caller plants its own messages at — see
// budgetTestInstant.
func crossScopeMessageCost(t *testing.T, at time.Time) (withField, withoutField int) {
	t.Helper()
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantCrossScopeInbox(t, dataDir, sid, "msg-000000000000", at)

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	require.Len(t, p.Messages, 1)
	m := p.Messages[0]
	require.NotEmpty(t, m.FromAddressShellArg, "the fixture must really be cross-scope, or this measures nothing")

	bare := m
	bare.FromAddressShellArg = ""
	return serializedSize(m), serializedSize(bare)
}

// TestNext_TheShellArgIsInsideThePageBudget pins what is true by construction
// today and was never checked: serializedSize marshals the whole struct, so a
// field added to the emitted message is budgeted along with the rest.
//
// The regression is written at the ONE budget that tells the two apart — exactly
// what the page cost BEFORE the field existed. A field emitted outside the
// budget would let that page through unchanged; being inside it, the second
// message no longer fits and is declared in hasMore instead of quietly
// inflating a limit that exists to protect the reader's context.
func TestNext_TheShellArgIsInsideThePageBudget(t *testing.T) {
	base := budgetTestInstant()
	withField, withoutField := crossScopeMessageCost(t, base)
	require.Greater(t, withField, withoutField, "the field must cost something")
	require.LessOrEqual(t, withField, 2*withoutField,
		"one message must still fit in the tight budget, or this exercises the oversize path instead")

	ids := []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}

	t.Run("at the pre-field budget the second message no longer fits", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		for i, id := range ids {
			plantCrossScopeInbox(t, dataDir, sid, id, base.Add(time.Duration(i)*time.Second))
		}
		cfg.MaxPageBytes = 2 * withoutField // both would fit if the field were free

		p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
		assert.Equal(t, 2, p.Total)
		assert.Equal(t, 1, p.Returned, "the field is counted, so the pair no longer fits")
		assert.True(t, p.HasMore, "and what did not fit is declared, not dropped")
	})

	t.Run("with room for the field both fit", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		for i, id := range ids {
			plantCrossScopeInbox(t, dataDir, sid, id, base.Add(time.Duration(i)*time.Second))
		}
		cfg.MaxPageBytes = 2 * withField

		p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
		assert.Equal(t, 2, p.Returned, "the budget is the only thing that moved")
		assert.False(t, p.HasMore)
	})
}
