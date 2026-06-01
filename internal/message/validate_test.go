package message

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validMessage() *Message {
	return &Message{
		ID:            "msg-abc123def456",
		SchemaVersion: SchemaVersionV2,
		From:          "abc123ef",
		FromRole:      "val",
		FromAgentName: "VAL-test",
		To:            "def456ab",
		ToRole:        "esc",
		Type:          TypeQuery,
		Timestamp:     "2026-05-24T18:00:00Z",
		Status:        StatusPending,
		Content:       "hello",
		InReplyTo:     nil,
		Metadata: Metadata{
			FromProject:     "cli-agents-bridge",
			ProcessingState: StatusPending,
		},
	}
}

func TestValidate_HappyPath(t *testing.T) {
	t.Parallel()
	require.NoError(t, Validate(validMessage(), 65536))
}

func TestValidate_RejectsInvalidMessageID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
	}{
		{"missing prefix", "abc123def456"},
		{"wrong prefix", "MSG-abc123def456"},
		{"too short", "msg-abc123"},
		{"too long", "msg-abc123def4567"},
		{"uppercase hex", "msg-ABC123DEF456"},
		{"path traversal", "msg-../etc/pas"},
		{"empty", ""},
		{"whitespace", "msg-abc123def4 6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validMessage()
			m.ID = tc.id
			err := Validate(m, 65536)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidMessageID)
		})
	}
}

func TestValidate_RejectsInvalidType(t *testing.T) {
	t.Parallel()

	m := validMessage()
	m.Type = "unknown-type"
	err := Validate(m, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidType)
}

func TestValidate_AcceptsAckType(t *testing.T) {
	t.Parallel()

	// F-12: ack is a first-class message type (auto-emitted delivery receipt).
	m := validMessage()
	m.Type = TypeAck
	require.NoError(t, Validate(m, 65536))
}

func TestValidate_RejectsInvalidStatus(t *testing.T) {
	t.Parallel()

	m := validMessage()
	m.Status = "wat"
	err := Validate(m, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStatus)
}

func TestValidate_RejectsBadFromToInReplyTo(t *testing.T) {
	t.Parallel()

	t.Run("from path traversal", func(t *testing.T) {
		m := validMessage()
		m.From = "../etc/passwd"
		err := Validate(m, 65536)
		require.Error(t, err)
		// from violates SC-4 — error wraps ErrInvalidSessionID from security
	})
	t.Run("to too short", func(t *testing.T) {
		m := validMessage()
		m.To = "abc"
		err := Validate(m, 65536)
		require.Error(t, err)
	})
	t.Run("inReplyTo bad shape", func(t *testing.T) {
		m := validMessage()
		bad := "not-a-msg-id"
		m.InReplyTo = &bad
		err := Validate(m, 65536)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidMessageID)
	})
	t.Run("inReplyTo nil is valid (non-reply message)", func(t *testing.T) {
		m := validMessage()
		m.InReplyTo = nil
		require.NoError(t, Validate(m, 65536))
	})
}

func TestValidate_RejectsOversizeContent(t *testing.T) {
	t.Parallel()

	m := validMessage()
	m.Content = strings.Repeat("x", 1001)
	err := Validate(m, 1000)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContentTooLarge)
}

func TestValidate_RejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	m := validMessage()
	m.SchemaVersion = 99
	err := Validate(m, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestEncodeStrict_RoundTrip(t *testing.T) {
	t.Parallel()

	in := validMessage()
	data, err := EncodeStrict(in, 65536)
	require.NoError(t, err)

	out, err := DecodeStrict(data, 65536)
	require.NoError(t, err)
	assert.Equal(t, in.ID, out.ID)
	assert.Equal(t, in.From, out.From)
	assert.Equal(t, in.Content, out.Content)
}

func TestDecodeStrict_RejectsUnknownField(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"id": "msg-abc123def456",
		"schemaVersion": 2,
		"from": "abc123ef",
		"fromRole": "val",
		"fromAgentName": "VAL",
		"to": "def456ab",
		"toRole": "esc",
		"type": "query",
		"timestamp": "2026-05-24T18:00:00Z",
		"status": "pending",
		"content": "hi",
		"inReplyTo": null,
		"metadata": {"fromProject": "x", "processingState": "pending"},
		"unexpectedField": "boom"
	}`)
	_, err := DecodeStrict(payload, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownField,
		"strict decoder must reject unknown top-level fields")
}

func TestDecodeLenient_AcceptsUnknownField(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"id": "msg-abc123def456",
		"schemaVersion": 2,
		"from": "abc123ef",
		"fromRole": "val",
		"fromAgentName": "VAL",
		"to": "def456ab",
		"toRole": "esc",
		"type": "query",
		"timestamp": "2026-05-24T18:00:00Z",
		"status": "pending",
		"content": "hi",
		"inReplyTo": null,
		"metadata": {"fromProject": "x", "processingState": "pending"},
		"forwardCompatField": "ok"
	}`)
	m, err := DecodeLenient(payload, 65536)
	require.NoError(t, err, "lenient decoder must tolerate forward-compatible additive fields")
	assert.Equal(t, "msg-abc123def456", m.ID)
}

// F-12 forward-compat: a peer running a future schema may send a message type
// this version does not know yet. The lenient runtime reader MUST deliver it
// (the consumer reads content and decides) — only the strict write/audit
// gateway rejects unknown types.
func TestDecodeLenient_AcceptsUnknownType(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"id": "msg-abc123def456",
		"schemaVersion": 2,
		"from": "abc123ef",
		"fromRole": "val",
		"fromAgentName": "VAL",
		"to": "def456ab",
		"toRole": "esc",
		"type": "future-type-v3",
		"timestamp": "2026-05-24T18:00:00Z",
		"status": "pending",
		"content": "hi",
		"inReplyTo": null,
		"metadata": {"fromProject": "x", "processingState": "pending"}
	}`)
	m, err := DecodeLenient(payload, 65536)
	require.NoError(t, err, "lenient decoder must tolerate an unknown (forward-compat) message type")
	assert.Equal(t, "future-type-v3", m.Type)
}

func TestDecodeStrict_RejectsUnknownType(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"id": "msg-abc123def456",
		"schemaVersion": 2,
		"from": "abc123ef",
		"fromRole": "val",
		"fromAgentName": "VAL",
		"to": "def456ab",
		"toRole": "esc",
		"type": "future-type-v3",
		"timestamp": "2026-05-24T18:00:00Z",
		"status": "pending",
		"content": "hi",
		"inReplyTo": null,
		"metadata": {"fromProject": "x", "processingState": "pending"}
	}`)
	_, err := DecodeStrict(payload, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidType,
		"strict decoder must reject an unknown type (protocol drift)")
}

func TestDecodeStrict_AppliesV1Defaults(t *testing.T) {
	t.Parallel()

	// v1 messages have no fromRole/toRole/metadata — defaults must populate
	payload := []byte(`{
		"id": "msg-abc123def456",
		"schemaVersion": 1,
		"from": "abc123ef",
		"to": "def456ab",
		"fromAgentName": "legacy",
		"type": "query",
		"timestamp": "2026-01-01T00:00:00Z",
		"status": "pending",
		"content": "v1 hello",
		"inReplyTo": null,
		"fromRole": "",
		"toRole": "",
		"metadata": {"fromProject": "", "processingState": ""}
	}`)
	m, err := DecodeStrict(payload, 65536)
	require.NoError(t, err)
	assert.Equal(t, "neutral", m.FromRole, "v1 read must default fromRole to neutral")
	assert.Equal(t, "neutral", m.ToRole, "v1 read must default toRole to neutral")
	assert.Equal(t, StatusPending, m.Metadata.ProcessingState,
		"v1 read must default processingState to pending")
}

func TestGenerateMessageID_MatchesRegex(t *testing.T) {
	t.Parallel()

	for i := 0; i < 100; i++ {
		id, err := GenerateMessageID()
		require.NoError(t, err)
		assert.True(t, messageIDPattern.MatchString(id),
			"generated id %q must satisfy msg-[a-z0-9]{12}", id)
	}
}

func TestValidateMessageID(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateMessageID("msg-abc123def456"))

	for _, bad := range []string{
		"",                  // empty
		"abc123def456",      // missing msg- prefix
		"msg-ABC123DEF456",  // uppercase not allowed
		"msg-abc123",        // too short
		"msg-abc123def4567", // too long
		"msg-abc12_def456",  // illegal char
	} {
		err := ValidateMessageID(bad)
		require.Error(t, err, "id %q must be rejected", bad)
		assert.ErrorIs(t, err, ErrInvalidMessageID)
	}
}
