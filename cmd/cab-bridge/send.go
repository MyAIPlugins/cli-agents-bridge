package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/routing"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// sendMessage composes, validates and atomically delivers a message from
// fromSID to `to`, returning the generated message ID. It is the single
// routing+encode+atomic-write path shared by `ask` (CLI send) and listen's
// auto-ack.
//
// Roles are resolved from the on-disk manifests of BOTH endpoints, never copied
// from any inbound message (F-12 vincolo A: an inbound message carries the
// SENDER's view of from/to roles, which are inverted from our perspective when
// we reply). Routing is therefore always evaluated against the real, current
// roles of the two sessions.
func sendMessage(cfg config.Config, mgr *session.Manager, fromSID, to, msgType, content string, inReplyTo *string, allowMesh bool) (string, error) {
	if err := security.ValidateSessionID(to); err != nil {
		return "", fmt.Errorf("send: to: %w", err)
	}
	senderManifest, err := mgr.LoadManifest(fromSID)
	if err != nil {
		return "", fmt.Errorf("send: load sender manifest: %w", err)
	}
	targetManifest, err := mgr.LoadManifest(to)
	if err != nil {
		return "", fmt.Errorf("send: load target manifest %q: %w", to, err)
	}

	if err := routing.ValidateSendPair(senderManifest.Role, targetManifest.Role, allowMesh); err != nil {
		return "", err
	}

	msgID, err := message.GenerateMessageID()
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}

	m := &message.Message{
		ID:            msgID,
		SchemaVersion: message.SchemaVersionV2,
		From:          fromSID,
		FromRole:      senderManifest.Role,
		FromAgentName: senderManifest.AgentName,
		To:            to,
		ToRole:        targetManifest.Role,
		Type:          msgType,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Status:        message.StatusPending,
		Content:       content,
		InReplyTo:     inReplyTo,
		Metadata: message.Metadata{
			FromProject:     senderManifest.ProjectName,
			ProcessingState: message.StatusPending,
		},
	}

	data, err := message.EncodeStrict(m, cfg.MaxMessageBytes)
	if err != nil {
		return "", err
	}

	targetInbox := filepath.Join(cfg.DataDir, "sessions", to, "inbox")
	if err := os.MkdirAll(targetInbox, 0o700); err != nil {
		return "", fmt.Errorf("send: mkdir target inbox: %w", err)
	}
	dst := filepath.Join(targetInbox, msgID+".json")
	if err := transportfs.AtomicWriteBytes(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("send: write message: %w", err)
	}

	// F-9: best-effort copy into the SENDER's outbox so the agent can verify its
	// own sends (outboxCount becomes meaningful, `cab sent` lists them). The real
	// delivery (target inbox above) has already succeeded — a failed outbox copy
	// must NOT fail the send, so errors are logged and swallowed, same posture as
	// the auto-ack and heartbeat goroutine. msgID is returned regardless.
	senderOutbox := filepath.Join(cfg.DataDir, "sessions", fromSID, "outbox")
	if mkErr := os.MkdirAll(senderOutbox, 0o700); mkErr != nil {
		fmt.Fprintf(os.Stderr, "cab-bridge: send: mkdir sender outbox (non-fatal): %v\n", mkErr)
	} else if cpErr := transportfs.AtomicWriteBytes(filepath.Join(senderOutbox, msgID+".json"), data, 0o600); cpErr != nil {
		fmt.Fprintf(os.Stderr, "cab-bridge: send: outbox copy for %s (non-fatal): %v\n", msgID, cpErr)
	}
	return msgID, nil
}

// autoAckTypes is the allow-list of inbound message types that trigger an
// automatic delivery receipt on consume. ONLY query: a brief is a query, and
// acking only queries makes an ack loop structurally impossible — the receipt
// we send is type=ack, never query, so no listener ever re-acks it — while
// keeping responses, notifies, pings and acks themselves quiet. (XEP-0184 /
// TCP / RFC 8098 all converge on "a receipt must never beget a receipt".)
var autoAckTypes = map[string]struct{}{
	message.TypeQuery: {},
}

// maybeAutoAck sends a best-effort delivery receipt to m's sender when m is an
// ack-worthy type. F-12: gives the orchestrator the inviato->ack->done state
// machine without manual discipline.
//
// Non-fatal by design: a failed ack (sender deregistered, or routing forbids
// the send because the listener is an observer / would be esc->esc without
// --allow-mesh) must never break the listen loop, so every error is logged to
// stderr and swallowed — same pattern as runAutoGC and the heartbeat goroutine.
//
// Idempotency: PollInbox moves a message to processed/ BEFORE emitting it, so a
// message is handed to the consumer at most once, even across a listen restart
// — there is no re-emit path, hence no double-ack. (Were re-emit ever
// reintroduced, the receiver tolerates duplicate acks idempotently: each
// carries the original message id via inReplyTo.)
//
// The receipt sets inReplyTo=m.ID for correlation. scanForReply skips type=ack,
// so this receipt does NOT satisfy a `receive --msg-id=<m.ID>` that is waiting
// for the real response; it stays in the sender's inbox as the observable F-12
// state signal (F-12 §3.3).
func maybeAutoAck(cfg config.Config, mgr *session.Manager, listenerSID string, m *message.Message) {
	if _, ok := autoAckTypes[m.Type]; !ok {
		return
	}
	inReplyTo := m.ID
	content := fmt.Sprintf("ACK %s: received", m.ID)
	if _, err := sendMessage(cfg, mgr, listenerSID, m.From, message.TypeAck, content, &inReplyTo, false); err != nil {
		fmt.Fprintf(os.Stderr, "cab-bridge: listen: auto-ack to %s for %s skipped: %v\n", m.From, m.ID, err)
	}
}
