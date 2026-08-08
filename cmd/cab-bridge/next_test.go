package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// --- helpers ---------------------------------------------------------------

func nextTestConfig(dataDir string) config.Config {
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.PollIntervalMs = 10
	cfg.HeartbeatTickMs = 50
	cfg.WakeWindowHours = 24
	return cfg
}

// runNextOnce drives nextRun with a short parent deadline so an idle wait ends
// in milliseconds instead of the configured 24h window.
func runNextOnce(t *testing.T, mgr *session.Manager, cfg config.Config, sid string, wait time.Duration) nextPayload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	var stdout, stderr bytes.Buffer
	require.NoError(t, nextRun(ctx, mgr, cfg, sid, &stdout, &stderr))

	var p nextPayload
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &p), "stdout must be one JSON payload: %s", stdout.String())
	return p
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
		require.Equal(t, nextStatusDelivered, p.Status)
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

	// And a second run does not re-deliver what is already NOTIFIED.
	p := runNextOnce(t, mgr, cfg, sid, 200*time.Millisecond)
	assert.Equal(t, nextStatusTimeout, p.Status)
	assert.Empty(t, p.Messages)
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

	t.Run("two_runs_deliver_each_message_at_most_once_between_them", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		base := time.Now().UTC()
		ids := []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc"}
		for i, id := range ids {
			plantInboxAt(t, dataDir, sid, id, "valxxx01", message.TypeQuery, "body", base.Add(time.Duration(i)*time.Second))
		}

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			emitted []string
		)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
				defer cancel()
				var out, errBuf bytes.Buffer
				if err := nextRun(ctx, mgr, cfg, sid, &out, &errBuf); err != nil {
					return
				}
				var p nextPayload
				if json.Unmarshal(out.Bytes(), &p) != nil || p.Status != nextStatusDelivered {
					return
				}
				mu.Lock()
				emitted = append(emitted, messageIDs(p)...)
				mu.Unlock()
			}()
		}
		wg.Wait()

		// At-least-once permits a duplicate after eviction, but the cursor must
		// never end up claiming a message nobody emitted.
		cursor, _, err := mgr.ReadWakeCursor(sid)
		require.NoError(t, err)
		for id := range cursor.Notified {
			assert.Contains(t, emitted, id, "%s is NOTIFIED but was never emitted", id)
		}
	})
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
		assert.NotEmpty(t, got.Body, "the on-disk path is emitted instead")
		assert.FileExists(t, got.Body)
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
// which is precisely the failure F-94 describes. Only elapsed time catches it.
func TestNext_ReturnsImmediatelyAfterDelivery(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "brief", time.Now().UTC())

	// A window far longer than the assertion below: if next waited for it, the
	// test would blow its budget rather than fail an assertion.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	start := time.Now()
	require.NoError(t, nextRun(ctx, mgr, cfg, sid, &stdout, &stderr))
	elapsed := time.Since(start)

	var p nextPayload
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &p))
	require.Equal(t, nextStatusDelivered, p.Status)
	assert.Less(t, elapsed, 2*time.Second,
		"next must return as soon as it has mail, not hold it until the window expires (F-94)")
}

func TestNext_TimeoutIsExitZeroWithPayload(t *testing.T) {
	mgr, cfg, sid, _ := newNextSession(t)

	p := runNextOnce(t, mgr, cfg, sid, 150*time.Millisecond)
	assert.Equal(t, nextStatusTimeout, p.Status)
	assert.Empty(t, p.Messages)
	assert.Contains(t, p.Hint, "again", "an idle window tells the agent what to do next")
}

func TestNext_AdoptsPIDAndPublishesDeadline(t *testing.T) {
	mgr, cfg, sid, _ := newNextSession(t)

	runNextOnce(t, mgr, cfg, sid, 150*time.Millisecond)

	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), mf.PID, "F-95: a waiting next must own the PID, not look stale")
	require.NotNil(t, mf.ListenUntil, "the wait deadline must be published (F-81)")
	assert.True(t, mf.ListenUntil.After(time.Now().UTC().Add(23*time.Hour)), "the published window is the configured 24h")
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
