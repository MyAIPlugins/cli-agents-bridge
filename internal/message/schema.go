// Package message implements the cli-agents-bridge message schema v2 plus
// strict validation. The on-wire format (JSON in inbox/outbox files) and the
// in-memory Go struct are kept aligned with PLAN §4.4 trimmed.
//
// Schema is additive — v1 messages from Patil upstream are readable with
// safe defaults applied via ApplyV1Defaults (PLAN §6.1 backward-compat).
// Writes always emit schemaVersion=2.
package message

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// SchemaVersionV2 is the wire format emitted by cli-agents-bridge v0.2+.
const SchemaVersionV2 = 2

// Message types (PLAN §4.4 enum). TypeAck (F-12) is a lightweight delivery
// receipt emitted automatically by listen when it hands a query to its
// consumer, so an orchestrator can tell "received" from "lost" without manual
// discipline. It must never itself trigger an ack (loop prevention) — see the
// auto-ack allow-list in cmd/cab-bridge/listen.go.
const (
	TypeQuery    = "query"
	TypeResponse = "response"
	TypePing     = "ping"
	TypeNotify   = "notify"
	TypeEvent    = "event"
	TypeAck      = "ack"
)

// Message statuses. MVP uses pending/processing/completed; "failed" is
// reserved for v0.3+ retry semantics.
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// validTypes / validStatuses are the canonical enum sets used by Validate.
// Order is irrelevant — we use them as set lookups.
var (
	validTypes = map[string]struct{}{
		TypeQuery: {}, TypeResponse: {}, TypePing: {}, TypeNotify: {}, TypeEvent: {}, TypeAck: {},
	}
	validStatuses = map[string]struct{}{
		StatusPending: {}, StatusProcessing: {}, StatusCompleted: {}, StatusFailed: {},
	}
)

// Message is the v2 on-disk JSON shape (PLAN §4.4 trimmed). The struct order
// mirrors the canonical JSON ordering so writers produce deterministic
// output, easier to diff under audit.
//
// Pointer-vs-value choice: inReplyTo is *string because nil renders as
// "inReplyTo": null in JSON (semantically distinct from "" empty-but-present).
// The Patil upstream format uses null, and PLAN keeps the convention.
type Message struct {
	ID               string   `json:"id"`
	SchemaVersion    int      `json:"schemaVersion"`
	From             string   `json:"from"`
	FromRole         string   `json:"fromRole"`
	FromAgentName    string   `json:"fromAgentName"`
	To               string   `json:"to"`
	ToRole           string   `json:"toRole"`
	Type             string   `json:"type"`
	Timestamp        string   `json:"timestamp"`
	Status           string   `json:"status"`
	Content          string   `json:"content"`
	InReplyTo        *string  `json:"inReplyTo"`
	Metadata         Metadata `json:"metadata"`
}

// Metadata is the inner object reserved for routing/observability fields
// that should not pollute the top-level schema. MVP carries only
// fromProject + processingState; v0.3+ may add threadId, retries, etc.
type Metadata struct {
	FromProject     string `json:"fromProject"`
	ProcessingState string `json:"processingState"`
}

// ApplyV1Defaults populates v2-only fields with safe defaults when reading
// a v1 message (Patil format). Called by the decoder when SchemaVersion == 1.
//
// Defaults rationale:
//   - FromRole/ToRole = "neutral": the v1 sender did not declare a role;
//     "neutral" is the same fallback used by session.ApplyV1Defaults so
//     routing rules can reject neutral→neutral as ambiguous (Sprint 3 task).
//   - Metadata.ProcessingState = "pending": conservative assumption for an
//     unparsed legacy message.
//   - InReplyTo stays nil if missing (the JSON decoder leaves the pointer
//     unset, which is the v1 default for non-reply messages).
func (m *Message) ApplyV1Defaults() {
	if m.FromRole == "" {
		m.FromRole = "neutral"
	}
	if m.ToRole == "" {
		m.ToRole = "neutral"
	}
	if m.Metadata.ProcessingState == "" {
		m.Metadata.ProcessingState = StatusPending
	}
}

// GenerateMessageID returns a new message ID matching the regex
// ^msg-[a-z0-9]{12}$ used by Validate. 6 bytes = 12 hex chars = 2^48
// possibilities; collision negligible for single-machine workloads.
func GenerateMessageID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate message id: %w", err)
	}
	return "msg-" + hex.EncodeToString(b[:]), nil
}
