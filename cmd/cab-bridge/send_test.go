package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCfgMgr(t *testing.T) (config.Config, *session.Manager, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := session.NewManager(dataDir, time.Second)
	return cfg, mgr, dataDir
}

// plantRoleSession writes a manifest with an explicit role under
// dataDir/sessions/<id>/ (the package-shared plantSession in register_test.go
// hardcodes RoleEsc; the send/auto-ack tests need val/esc/observer roles).
func plantRoleSession(t *testing.T, dataDir, id, role string) {
	t.Helper()
	sessionDir := filepath.Join(dataDir, "sessions", id)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	mgr := session.NewManager(dataDir, time.Second)
	now := time.Now().UTC()
	mf := &session.Manifest{
		SessionID:     id,
		SchemaVersion: session.SchemaVersionV2,
		ProjectName:   "proj-" + id,
		ProjectPath:   filepath.Join(dataDir, "proj-"+id),
		AgentName:     "agent-" + id,
		Role:          role,
		PID:           os.Getpid(),
		StartedAt:     now,
		LastHeartbeat: now,
		Status:        session.StatusActive,
		Capabilities:  []string{"query"},
	}
	require.NoError(t, mgr.SaveManifest(mf))
}

func countInboxJSON(t *testing.T, dataDir, sessionID string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions", sessionID, "inbox"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

func firstInboxMsg(t *testing.T, dataDir, sessionID string) *message.Message {
	t.Helper()
	inbox := filepath.Join(dataDir, "sessions", sessionID, "inbox")
	entries, err := os.ReadDir(inbox)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(inbox, e.Name()))
		require.NoError(t, err)
		m, err := message.DecodeLenient(data, 65536)
		require.NoError(t, err)
		return m
	}
	t.Fatalf("no message in %s inbox", sessionID)
	return nil
}

// TestSendMessage_ResolvesRolesFromManifests verifies vincolo A: the delivered
// message carries from/to roles resolved from the on-disk manifests of both
// endpoints, never copied from any inbound message.
func TestSendMessage_ResolvesRolesFromManifests(t *testing.T) {
	t.Parallel()

	cfg, mgr, dataDir := testCfgMgr(t)
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	id, err := sendMessage(cfg, mgr, "valsess1", "escsess1", message.TypeQuery, "hi", nil, false)
	require.NoError(t, err)
	require.NotEmpty(t, id)

	m := firstInboxMsg(t, dataDir, "escsess1")
	assert.Equal(t, id, m.ID)
	assert.Equal(t, "valsess1", m.From)
	assert.Equal(t, "escsess1", m.To)
	assert.Equal(t, session.RoleVal, m.FromRole, "fromRole resolved from sender manifest")
	assert.Equal(t, session.RoleEsc, m.ToRole, "toRole resolved from target manifest")
	assert.Equal(t, message.TypeQuery, m.Type)
}

// TestSendMessage_PopulatesSenderOutbox: F-9 — after a send, a copy of the
// message lands in the SENDER's outbox so the agent can verify its own sends.
func TestSendMessage_PopulatesSenderOutbox(t *testing.T) {
	t.Parallel()

	cfg, mgr, dataDir := testCfgMgr(t)
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	id, err := sendMessage(cfg, mgr, "valsess1", "escsess1", message.TypeQuery, "hi", nil, false)
	require.NoError(t, err)

	data, rerr := os.ReadFile(filepath.Join(dataDir, "sessions", "valsess1", "outbox", id+".json"))
	require.NoError(t, rerr, "sender outbox must contain a copy of the sent message")
	m, derr := message.DecodeLenient(data, 65536)
	require.NoError(t, derr)
	assert.Equal(t, id, m.ID)
	assert.Equal(t, "escsess1", m.To, "outbox copy records the recipient")
	assert.Equal(t, message.TypeQuery, m.Type)
}

// TestSendMessage_OutboxCopyNonFatal is the punto-1 guard: the outbox copy is
// best-effort. We sabotage the sender's outbox (a FILE where the dir should be,
// so MkdirAll fails); sendMessage must STILL return the msgID with no error and
// the real delivery (recipient inbox) must have happened.
func TestSendMessage_OutboxCopyNonFatal(t *testing.T) {
	t.Parallel()

	cfg, mgr, dataDir := testCfgMgr(t)
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	// A file named "outbox" makes MkdirAll(sessions/valsess1/outbox) fail.
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "sessions", "valsess1", "outbox"), []byte("not a dir"), 0o600))

	id, err := sendMessage(cfg, mgr, "valsess1", "escsess1", message.TypeQuery, "hi", nil, false)
	require.NoError(t, err, "a failed outbox copy must NOT fail the send")
	require.NotEmpty(t, id, "msgID must be returned even when the outbox copy fails")

	require.Equal(t, 1, countInboxJSON(t, dataDir, "escsess1"), "recipient inbox must still have the message")
	assert.Equal(t, id, firstInboxMsg(t, dataDir, "escsess1").ID)
}

// TestMaybeAutoAck_AcksOnlyQuery is the loop-prevention + allow-list test: only
// a consumed query produces an auto-ack; ack/response/notify/ping/event do not
// (so an ack never begets an ack). When an ack is sent, it is type=ack with
// inReplyTo=<original id> and roles resolved from the manifests.
func TestMaybeAutoAck_AcksOnlyQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		msgType string
		wantAck bool
	}{
		{"query is acked", message.TypeQuery, true},
		{"ack is not acked (loop prevention)", message.TypeAck, false},
		{"response is not acked", message.TypeResponse, false},
		{"notify is not acked", message.TypeNotify, false},
		{"ping is not acked", message.TypePing, false},
		{"event is not acked", message.TypeEvent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, mgr, dataDir := testCfgMgr(t)
			plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
			plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

			inbound := &message.Message{
				ID:   "msg-aaaaaaaaaaaa",
				From: "valsess1",
				To:   "escsess1",
				Type: tc.msgType,
			}
			maybeAutoAck(cfg, mgr, "escsess1", inbound)

			got := countInboxJSON(t, dataDir, "valsess1")
			if !tc.wantAck {
				assert.Equal(t, 0, got, "no auto-ack must be sent for type %q", tc.msgType)
				return
			}
			require.Equal(t, 1, got, "exactly one auto-ack must reach the query sender")
			ack := firstInboxMsg(t, dataDir, "valsess1")
			assert.Equal(t, message.TypeAck, ack.Type)
			require.NotNil(t, ack.InReplyTo)
			assert.Equal(t, "msg-aaaaaaaaaaaa", *ack.InReplyTo, "ack correlates to the original message")
			assert.Equal(t, "escsess1", ack.From, "ack sent by the listener")
			assert.Equal(t, session.RoleEsc, ack.FromRole, "ack roles resolved from manifests, not inbound")
			assert.Equal(t, session.RoleVal, ack.ToRole)
		})
	}
}

// TestMaybeAutoAck_NonFatalWhenSenderMissing: a query whose sender manifest is
// gone must not panic and must silently skip the ack (best-effort).
func TestMaybeAutoAck_NonFatalWhenSenderMissing(t *testing.T) {
	t.Parallel()

	cfg, mgr, dataDir := testCfgMgr(t)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	inbound := &message.Message{ID: "msg-aaaaaaaaaaaa", From: "ghostses", To: "escsess1", Type: message.TypeQuery}
	maybeAutoAck(cfg, mgr, "escsess1", inbound) // must not panic
	assert.Equal(t, 0, countInboxJSON(t, dataDir, "ghostses"))
}

// TestMaybeAutoAck_NonFatalWhenObserverListener: an observer listener cannot
// send (routing), so the auto-ack is skipped without panic.
func TestMaybeAutoAck_NonFatalWhenObserverListener(t *testing.T) {
	t.Parallel()

	cfg, mgr, dataDir := testCfgMgr(t)
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "obssess1", session.RoleObserver)

	inbound := &message.Message{ID: "msg-aaaaaaaaaaaa", From: "valsess1", To: "obssess1", Type: message.TypeQuery}
	maybeAutoAck(cfg, mgr, "obssess1", inbound) // observer cannot send → skipped
	assert.Equal(t, 0, countInboxJSON(t, dataDir, "valsess1"))
}

// F-43: a second identical ask within the window warns on stderr but STILL
// delivers (default: lose nothing, never block a legitimate repeat).
func TestRunAsk_DuplicateDefaultWarnsAndSends(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	args := []string{"--session-id=valsess1", "--to=escsess1", "--content=hello dup"}
	var e1 error
	captureStdout(t, func() { e1 = runAsk(args) })
	require.NoError(t, e1)

	var e2 error
	_, stderr := captureStdoutStderr(t, func() { e2 = runAsk(args) })
	require.NoError(t, e2, "default duplicate must still send (exit 0)")
	assert.Contains(t, stderr, "duplicate", "a warning must be emitted on stderr")
	assert.Equal(t, 2, countInboxJSON(t, dataDir, "escsess1"), "default sends anyway: recipient has both")
}

// F-43: --skip-duplicate does not resend; it prints the ORIGINAL id (caller
// idempotence) and leaves the recipient inbox unchanged.
func TestRunAsk_DuplicateSkipReturnsOriginalID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	args := []string{"--session-id=valsess1", "--to=escsess1", "--content=hello dup"}
	var e1 error
	out1 := captureStdout(t, func() { e1 = runAsk(args) })
	require.NoError(t, e1)
	id1 := strings.TrimSpace(out1)

	var e2 error
	out2 := captureStdout(t, func() {
		e2 = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--content=hello dup", "--skip-duplicate"})
	})
	require.NoError(t, e2)
	assert.Equal(t, id1, strings.TrimSpace(out2), "skip prints the ORIGINAL id, not a new one")
	assert.Equal(t, 1, countInboxJSON(t, dataDir, "escsess1"), "skip must NOT resend")
}

// F-43: a different content within the window is not a duplicate — sent, no warning.
func TestRunAsk_DifferentContentNotDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	var e1 error
	captureStdout(t, func() { e1 = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--content=first"}) })
	require.NoError(t, e1)

	var e2 error
	_, stderr := captureStdoutStderr(t, func() {
		e2 = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--content=second"})
	})
	require.NoError(t, e2)
	assert.NotContains(t, stderr, "duplicate", "different content must not warn")
	assert.Equal(t, 2, countInboxJSON(t, dataDir, "escsess1"))
}

// F-43: CAB_DEDUP_WINDOW_SECONDS=0 disables the check — a duplicate is sent with
// no warning (the <=0 gate).
func TestRunAsk_DedupDisabled(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	t.Setenv("CAB_DEDUP_WINDOW_SECONDS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	args := []string{"--session-id=valsess1", "--to=escsess1", "--content=dup"}
	var e1, e2 error
	captureStdout(t, func() { e1 = runAsk(args) })
	require.NoError(t, e1)
	_, stderr := captureStdoutStderr(t, func() { e2 = runAsk(args) })
	require.NoError(t, e2)
	assert.NotContains(t, stderr, "duplicate", "dedup disabled → no warning")
	assert.Equal(t, 2, countInboxJSON(t, dataDir, "escsess1"))
}

// F-34: a still-unread non-ack from the recipient warns on stderr but STILL
// sends (never blocks), and the unread message is NOT consumed.
func TestRunAsk_UnreadWarnsAndSends(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)
	// ESC sent VAL a report that VAL never consumed; VAL never sent to ESC, so the
	// cutoff is zero and this counts as unread.
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeResponse, "ESC report", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))

	var e error
	_, stderr := captureStdoutStderr(t, func() {
		e = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--content=new brief"})
	})
	require.NoError(t, e, "unread warning never blocks the send")
	assert.Contains(t, stderr, "unread", "a warning citing the unread message must be emitted")
	assert.Contains(t, stderr, "msg-aaaaaaaaaaaa", "the warning cites the id so the caller can read it")
	assert.Equal(t, 1, countInboxJSON(t, dataDir, "escsess1"), "the brief is still delivered")
	assert.Equal(t, 1, countInboxJSON(t, dataDir, "valsess1"), "the unread message is NOT consumed (pure read)")
}

func TestRunAsk_NoUnreadWhenInboxClean(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	var e error
	_, stderr := captureStdoutStderr(t, func() {
		e = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--content=brief"})
	})
	require.NoError(t, e)
	assert.NotContains(t, stderr, "unread", "a clean inbox must not warn")
}

func TestRunAsk_NoUnreadForAckOnly(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeAck, "ACK", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))

	var e error
	_, stderr := captureStdoutStderr(t, func() {
		e = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--content=brief"})
	})
	require.NoError(t, e)
	assert.NotContains(t, stderr, "unread", "an ack-only inbox must not warn")
}

// F-34 cutoff: a peer message older than our last send to that peer is
// superseded and must not warn.
func TestRunAsk_NoUnreadWhenPendingOlderThanLastSent(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// ESC's pending is OLDER than VAL's last send to ESC → superseded.
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeResponse, "old report", base)
	plantOutboxAt(t, dataDir, "valsess1", "msg-bbbbbbbbbbbb", "escsess1", message.TypeQuery, "prev brief", base.Add(30*time.Second))

	var e error
	_, stderr := captureStdoutStderr(t, func() {
		e = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--content=new brief"})
	})
	require.NoError(t, e)
	assert.NotContains(t, stderr, "unread", "a peer message older than our last send is superseded, no warning")
}

// F-37: --in-reply-to to a message that EXISTS in our inbox/processed → no warn.
func TestRunAsk_InReplyToExisting_NoWarn(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)
	// a message we RECEIVED from esc, sitting in our processed/
	plantMsg(t, dataDir, "valsess1", "processed", "msg-aaaaaaaaaaaa", "escsess1", "ESC-y", message.TypeQuery, "brief we got")

	var e error
	_, stderr := captureStdoutStderr(t, func() {
		e = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--type=response", "--in-reply-to=msg-aaaaaaaaaaaa", "--content=my reply"})
	})
	require.NoError(t, e)
	assert.NotContains(t, stderr, "not found", "an existing in-reply-to must not warn")
	assert.Equal(t, 1, countInboxJSON(t, dataDir, "escsess1"), "the reply is delivered")
}

// F-37: --in-reply-to to a well-formed but non-existent id → warn + send (default).
func TestRunAsk_InReplyToHallucinated_WarnsAndSends(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	var e error
	_, stderr := captureStdoutStderr(t, func() {
		e = runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--type=response", "--in-reply-to=msg-ffffffffffff", "--content=reply"})
	})
	require.NoError(t, e, "a hallucinated in-reply-to warns but still sends by default")
	assert.Contains(t, stderr, "not found")
	assert.Equal(t, 1, countInboxJSON(t, dataDir, "escsess1"), "default still delivers")
}

// F-37: --strict-reply rejects a non-existent in-reply-to (does NOT send).
func TestRunAsk_InReplyToHallucinated_StrictRejects(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	err := runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--type=response", "--in-reply-to=msg-ffffffffffff", "--content=reply", "--strict-reply"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Equal(t, 0, countInboxJSON(t, dataDir, "escsess1"), "--strict-reply must NOT send")
}

// F-37: a malformed --in-reply-to gets a clean format error before the existence check.
func TestRunAsk_InReplyToMalformed_FormatError(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantRoleSession(t, dataDir, "valsess1", session.RoleVal)
	plantRoleSession(t, dataDir, "escsess1", session.RoleEsc)

	err := runAsk([]string{"--session-id=valsess1", "--to=escsess1", "--in-reply-to=notanid", "--content=x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, message.ErrInvalidMessageID)
	assert.Equal(t, 0, countInboxJSON(t, dataDir, "escsess1"))
}
