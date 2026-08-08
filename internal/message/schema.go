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

// Message types (PLAN §4.4 enum).
//
// TypeAck is RETIRED as of v0.8 (DESIGN §2.4): delivery receipts were noise the
// agent mistook for content, and the mailbox states replace them — `sent`
// derives the real state from the recipient's mailbox. It stays in this enum
// because messages already on disk must remain READABLE; what it may no longer
// be is WRITTEN (see writableTypes).
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
	// validTypes is the READ side: everything that may appear in a file we have
	// to be able to decode, retired types included.
	validTypes = map[string]struct{}{
		TypeQuery: {}, TypeResponse: {}, TypePing: {}, TypeNotify: {}, TypeEvent: {}, TypeAck: {},
	}
	// writableTypes is the WRITE side, and it is deliberately smaller. Splitting
	// the two is what lets a type be retired without turning every existing file
	// of that type into an unreadable one — decoding runs through validTypes, so
	// a single shrinking enum would have made yesterday's acks look corrupt.
	writableTypes = map[string]struct{}{
		TypeQuery: {}, TypeResponse: {}, TypePing: {}, TypeNotify: {}, TypeEvent: {},
	}
	validStatuses = map[string]struct{}{
		StatusPending: {}, StatusProcessing: {}, StatusCompleted: {}, StatusFailed: {},
	}
)

// IsValidType reports whether t is a canonical message type. Exported so the
// CLI input layer (ask) can validate a user-supplied --type against the SAME
// enum the schema gateway uses, without duplicating the set (DRY — no drift
// between the two). The CLI may present a narrower, user-facing list in its
// error text (e.g. ask omits the auto-emitted "ack"), but membership is decided
// here against validTypes.
func IsValidType(t string) bool {
	_, ok := writableTypes[t]
	return ok
}

// IsReadableType reports whether t can be decoded from a file on disk. It is a
// superset of IsValidType: retired types stay readable forever.
func IsReadableType(t string) bool {
	_, ok := validTypes[t]
	return ok
}

// Message is the v2 on-disk JSON shape (PLAN §4.4 trimmed). The struct order
// mirrors the canonical JSON ordering so writers produce deterministic
// output, easier to diff under audit.
//
// Pointer-vs-value choice: inReplyTo is *string because nil renders as
// "inReplyTo": null in JSON (semantically distinct from "" empty-but-present).
// The Patil upstream format uses null, and PLAN keeps the convention.
type Message struct {
	ID            string  `json:"id"`
	SchemaVersion int     `json:"schemaVersion"`
	From          string  `json:"from"`
	FromRole      string  `json:"fromRole"`
	FromAgentName string  `json:"fromAgentName"`
	To            string  `json:"to"`
	ToRole        string  `json:"toRole"`
	Type          string  `json:"type"`
	Timestamp     string  `json:"timestamp"`
	Status        string  `json:"status"`
	Content       string  `json:"content"`
	InReplyTo     *string `json:"inReplyTo"`
	// Closes lists every message this reply archives (DESIGN v0.8 §2.3). The
	// schema has a single InReplyTo, which carries the ANCHOR — the oldest open
	// ask of the set — while Closes carries the full set. Without it, a reply
	// that closes two asks would imply two identities for one response, while
	// the interface promises exactly one.
	//
	// omitempty: only a reply ever sets it, so every other message stays
	// byte-identical to what earlier versions produced.
	Closes   []string `json:"closes,omitempty"`
	Metadata Metadata `json:"metadata"`
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
