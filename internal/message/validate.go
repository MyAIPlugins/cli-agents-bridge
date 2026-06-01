package message

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// messageIDPattern enforces the wire format ^msg-[a-z0-9]{12}$. 12 hex chars
// after the literal "msg-" prefix. GenerateMessageID produces exactly this
// shape.
var messageIDPattern = regexp.MustCompile(`^msg-[a-z0-9]{12}$`)

// Validation errors. Sentinel values let callers branch with errors.Is
// without parsing error strings.
var (
	ErrInvalidMessageID   = errors.New("invalid message id: must match ^msg-[a-z0-9]{12}$")
	ErrInvalidType        = errors.New("invalid message type")
	ErrInvalidStatus      = errors.New("invalid message status")
	ErrContentTooLarge    = errors.New("message content exceeds MaxMessageBytes")
	ErrUnknownField       = errors.New("message JSON contains unknown field")
	ErrMissingRequired    = errors.New("message missing required field")
	ErrUnsupportedVersion = errors.New("unsupported schemaVersion")
)

// ValidateMessageID returns ErrInvalidMessageID if id does not match the wire
// format ^msg-[a-z0-9]{12}$. Exported for callers that accept a message id from
// user input (e.g. `cab-bridge read <msg-id>`) and must reject a malformed or
// fabricated id before using it (an F-37 safety net). Reuses messageIDPattern
// and the ErrInvalidMessageID sentinel that validateCommon already applies to
// the in-message id, so the rule never drifts between the two call sites.
func ValidateMessageID(id string) error {
	if !messageIDPattern.MatchString(id) {
		return fmt.Errorf("%w: got %q", ErrInvalidMessageID, id)
	}
	return nil
}

// Validate enforces all PLAN §4.4 constraints on m, INCLUDING the strict
// message-type enum. maxContentBytes is the per-message size limit from
// config.MaxMessageBytes — passed in rather than read from a global to keep
// this package free of config import cycles.
//
// Validate is the write/audit gateway check: it is called by EncodeStrict
// (pre-write) and DecodeStrict (audit read). An unknown type here is a hard
// error — it surfaces protocol drift or a typo before it reaches the wire
// (no silent fallback per CLAUDE.md "no fallback impliciti").
//
// Runtime read paths use validateCommon via DecodeLenient instead, which
// tolerates an unknown type for forward-compat (F-12) — see validateCommon.
func Validate(m *Message, maxContentBytes int) error {
	if err := validateCommon(m, maxContentBytes); err != nil {
		return err
	}
	if _, ok := validTypes[m.Type]; !ok {
		return fmt.Errorf("validate: %w: got %q (allowed: query|response|ping|notify|event|ack)",
			ErrInvalidType, m.Type)
	}
	return nil
}

// validateCommon runs every structural, security and size constraint EXCEPT
// the message-TYPE enum membership. The status enum stays strict here (F-12
// ratifica A: only the type enum grows additively in v0.2.2; status is
// unchanged). It is the shared core of the strict Validate and of the lenient
// read path: DecodeLenient calls validateCommon directly so a future peer can
// send a type this version does not know yet and still have its message
// delivered (the consumer reads content and decides). The strict gateway
// (Validate/EncodeStrict/DecodeStrict) still rejects unknown types.
func validateCommon(m *Message, maxContentBytes int) error {
	if m == nil {
		return fmt.Errorf("validate: %w: message is nil", ErrMissingRequired)
	}
	if m.SchemaVersion != SchemaVersionV2 && m.SchemaVersion != 1 {
		return fmt.Errorf("validate: %w: got schemaVersion=%d (supported: 1, 2)",
			ErrUnsupportedVersion, m.SchemaVersion)
	}
	if !messageIDPattern.MatchString(m.ID) {
		return fmt.Errorf("validate: %w: got %q", ErrInvalidMessageID, m.ID)
	}
	if err := security.ValidateSessionID(m.From); err != nil {
		return fmt.Errorf("validate: field from: %w", err)
	}
	if err := security.ValidateSessionID(m.To); err != nil {
		return fmt.Errorf("validate: field to: %w", err)
	}
	if m.InReplyTo != nil {
		if !messageIDPattern.MatchString(*m.InReplyTo) {
			return fmt.Errorf("validate: %w: inReplyTo=%q", ErrInvalidMessageID, *m.InReplyTo)
		}
	}
	if _, ok := validStatuses[m.Status]; !ok {
		return fmt.Errorf("validate: %w: got %q (allowed: pending|processing|completed|failed)",
			ErrInvalidStatus, m.Status)
	}
	if maxContentBytes > 0 && len(m.Content) > maxContentBytes {
		return fmt.Errorf("validate: %w: %d > %d", ErrContentTooLarge, len(m.Content), maxContentBytes)
	}
	if m.Timestamp == "" {
		return fmt.Errorf("validate: %w: timestamp empty", ErrMissingRequired)
	}
	return nil
}

// EncodeStrict marshals m into JSON after running Validate. Used at the
// validation gateway when writing a new outbound message. Returns the
// marshaled bytes ready for atomic write to inbox/<peer>/<id>.json.
//
// Note: EncodeStrict does NOT set DisallowUnknownFields — that flag applies
// only to decoding. Writers never produce unknown fields by construction.
func EncodeStrict(m *Message, maxContentBytes int) ([]byte, error) {
	if err := Validate(m, maxContentBytes); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode marshal: %w", err)
	}
	return data, nil
}

// DecodeStrict reads JSON bytes and decodes into a Message with
// DisallowUnknownFields semantics for the validation gateway path. Unknown
// fields are a hard error — this surfaces protocol drift (peer running an
// unsupported version, hand-crafted message with typos) instead of silently
// accepting payload.
//
// For runtime read paths that must accept additive schema changes from
// future peer versions, use DecodeLenient instead.
func DecodeStrict(data []byte, maxContentBytes int) (*Message, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Message
	if err := dec.Decode(&m); err != nil {
		return nil, wrapUnknownField(err)
	}
	if m.SchemaVersion == 1 {
		m.ApplyV1Defaults()
	}
	if err := Validate(&m, maxContentBytes); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &m, nil
}

// DecodeLenient reads JSON bytes ignoring unknown fields — used by runtime
// readers that must tolerate forward-compatible additive schema growth
// (e.g. a v0.3 peer adding "threadId" should not break a v0.2 reader).
//
// Applies validateCommon (NOT the strict Validate) after decode: required
// fields, the status enum, security and size limits are still enforced, but
// an unknown message TYPE is tolerated (F-12 forward-compat — a future peer
// may send a type this version does not know yet; the consumer reads content
// and decides). The strict write/audit gateway (EncodeStrict/DecodeStrict)
// still rejects unknown types.
func DecodeLenient(data []byte, maxContentBytes int) (*Message, error) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode lenient: %w", err)
	}
	if m.SchemaVersion == 1 {
		m.ApplyV1Defaults()
	}
	if err := validateCommon(&m, maxContentBytes); err != nil {
		return nil, fmt.Errorf("decode lenient: %w", err)
	}
	return &m, nil
}

// wrapUnknownField promotes the json package's "unknown field ..." text
// error into our typed sentinel ErrUnknownField so callers can errors.Is.
func wrapUnknownField(err error) error {
	if err == nil {
		return nil
	}
	// json's unknown-field error wraps the literal phrase
	// "json: unknown field". String-match is acceptable here because the
	// json stdlib has emitted this exact phrase since Go 1.10 with a
	// compatibility promise.
	if errMsg := err.Error(); len(errMsg) > 0 && containsUnknownFieldHint(errMsg) {
		return fmt.Errorf("%w: %v", ErrUnknownField, err)
	}
	return fmt.Errorf("decode: %w", err)
}

func containsUnknownFieldHint(s string) bool {
	const hint = "json: unknown field"
	if len(s) < len(hint) {
		return false
	}
	// stdlib emits the hint as a prefix
	return s[:len(hint)] == hint
}
