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
	// F-116, and this is where the restriction BINDS: the scopes and roles of the
	// two manifests being used to compose this very message. The resolver checks
	// earlier so the error arrives before anything is written, but a check that
	// runs on a lookup is a warning — `SetRole` sits between the two and F-110
	// made it part of the ordinary path (CRI diff-gate P1-3).
	//
	// Cross-scope is decided by comparing the scopes, never by how the address
	// was spelled: qualifying a peer in one's OWN project is a long way of
	// writing a local message, not a crossing.
	senderScope, _ := effectiveScope(senderManifest)
	targetScope, _ := effectiveScope(targetManifest)
	if crossesScopes(senderScope, targetScope) {
		if err := allowedAcrossScopes(senderManifest.Role, targetManifest.Role, targetManifest.AgentName, scopeLabelOf(targetManifest.Scope, nil)); err != nil {
			return "", err
		}
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
			FromScope:       senderManifest.Scope,
			ProcessingState: message.StatusPending,
		},
	}

	data, err := message.EncodeStrict(m, cfg.MaxMessageBytes)
	if err != nil {
		return "", err
	}

	// The delivery runs inside the TARGET's session lock, and re-validates the
	// target manifest in there (CRI diff-gate 1c P0).
	//
	// deliverResponse already did this, but the ORDINARY path did not: a lock
	// protects only against whoever respects it. cleanup could take the lock and
	// snapshot inbox/ while this send — holding nothing — wrote its file, and the
	// following RemoveAll deleted a message the sender had just been told was
	// delivered. Exit 0 on one side, nothing ever received on the other.
	targetInbox := filepath.Join(cfg.DataDir, "sessions", to, "inbox")
	if err := mgr.WithSessionLock(to, func() error {
		if _, rerr := mgr.LoadManifest(to); rerr != nil {
			return fmt.Errorf("recipient %s disappeared before delivery: %w", to, rerr)
		}
		if merr := os.MkdirAll(targetInbox, 0o700); merr != nil {
			return fmt.Errorf("mkdir target inbox: %w", merr)
		}
		return transportfs.AtomicWriteBytes(filepath.Join(targetInbox, msgID+".json"), data, 0o600)
	}); err != nil {
		return "", fmt.Errorf("send: %w", err)
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
