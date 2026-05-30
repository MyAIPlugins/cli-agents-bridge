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
