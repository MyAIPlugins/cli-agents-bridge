package findings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validInput = `{"lotto":"F-156","gate":"piano","critico":"CRI","chiave":"lock-race","finding":"the lock is advisory","severita":"Major","evidenza":"lock.go:12","decisione":"PROPOSE-FIX","esito":"aperto"}`

func TestDecodeStrict_ReadsTheColumns(t *testing.T) {
	in, err := DecodeStrict(strings.NewReader(validInput))
	require.NoError(t, err)
	assert.Equal(t, "F-156", in.Lotto)
	assert.Equal(t, "piano", in.Gate)
	assert.Equal(t, "lock-race", in.Chiave)
	assert.Equal(t, "", in.Azione)
	assert.Empty(t, in.Ts)
	assert.Empty(t, in.FirstSeen)
	assert.Empty(t, Validate(in, nil))
}

func TestDecodeStrict_TokenAndCostoAcceptANumberOrAStringWithANumber(t *testing.T) {
	for _, body := range []string{
		`"token":5000,"costo":1.25`,
		`"token":"5000","costo":"1.25"`,
	} {
		in, err := DecodeStrict(strings.NewReader(strings.Replace(validInput, `"esito":"aperto"`, `"esito":"aperto",`+body, 1)))
		require.NoError(t, err, body)
		assert.Equal(t, "5000", in.Token.String())
		assert.Equal(t, "1.25", in.Costo.String())
		assert.Empty(t, Validate(in, nil))
	}
}

func TestDecodeStrict_RefusesTsAndFirstSeenFromTheCaller(t *testing.T) {
	for column, body := range map[string]string{
		"ts":         `"ts":"2026-11-08T10:00:00.000Z"`,
		"first_seen": `"first_seen":"2026-11-08T10:00:00.000Z"`,
	} {
		_, err := DecodeStrict(strings.NewReader(strings.Replace(validInput, `"esito":"aperto"`, `"esito":"aperto",`+body, 1)))
		require.Error(t, err, column)
		assert.Equal(t, "findings: column "+column+" is written by the command", err.Error())
	}
}

// Giving the key at all is the refusal, whatever the value: null, an empty
// string, a number, an object. A JSON null used to read as "not given" because
// the field was a *string (encoding/json leaves a pointer nil on null), so
// `"ts": null` passed and the row was recorded as if the key were absent.
func TestDecodeStrict_AnyValueForTsOrFirstSeenIsTheSameRefusal(t *testing.T) {
	values := []string{`null`, `""`, `"2026-11-08T10:00:00.000Z"`, `5`, `true`, `[]`, `{}`}
	for _, column := range []string{"ts", "first_seen"} {
		for _, value := range values {
			body := `"` + column + `":` + value
			_, err := DecodeStrict(strings.NewReader(strings.Replace(validInput, `"esito":"aperto"`, `"esito":"aperto",`+body, 1)))
			require.Error(t, err, body)
			assert.Equal(t, "findings: column "+column+" is written by the command", err.Error(), body)
		}
	}
}

func TestDecodeStrict_ErrorsAreFixedTextsAndNeverCarryTheInput(t *testing.T) {
	// "ZQ9" marks the value: it must never come back in a message.
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", ``, "findings: input is empty"},
		{"blank", " \n\t ", "findings: input is empty"},
		{"truncated", `{"lotto":"ZQ9`, "findings: stdin is not valid JSON (byte offset "},
		{"syntax", `{"lotto":ZQ9}`, "findings: stdin is not valid JSON (byte offset "},
		{"array", `["ZQ9"]`, "findings: stdin must be one JSON object"},
		{"string", `"ZQ9"`, "findings: stdin must be one JSON object"},
		{"number", `5`, "findings: stdin must be one JSON object"},
		{"unknown key", `{"ZQ9":"x"}`, "findings: unknown column; the columns are: lotto, gate, critico, chiave, finding, severita, evidenza, decisione, azione, esito, modello, token, costo"},
		{"number in a text column", `{"lotto":5}`, "findings: column lotto must be a string"},
		{"array in a text column", `{"finding":["ZQ9"]}`, "findings: column finding must be a string"},
		{"object in a text column", `{"finding":{"a":"ZQ9"}}`, "findings: column finding must be a string"},
		{"bad number string", `{"token":"ZQ9"}`, "findings: columns token and costo must be a number or a string holding a number"},
		{"bool as number", `{"costo":true}`, "findings: columns token and costo must be a number or a string holding a number"},
		{"two values", `{} {"ZQ9":1}`, "findings: more than one JSON value on stdin"},
		{"trailing text", `{} ZQ9`, "findings: more than one JSON value on stdin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeStrict(strings.NewReader(tc.input))
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tc.want), "got %q, want prefix %q", err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "ZQ9")
			assert.NotContains(t, err.Error(), "json:", "the message of encoding/json must not be forwarded")
		})
	}
}

func TestDecodeStrict_InputOverTheLimitIsRefused(t *testing.T) {
	big := `{"finding":"` + strings.Repeat("a", maxInputBytes) + `"}`
	_, err := DecodeStrict(strings.NewReader(big))
	require.Error(t, err)
	assert.Equal(t, "findings: input is larger than 65536 bytes", err.Error())

	// Exactly at the limit it is read (and then fails for what it contains, not for its size).
	padded := `{"finding":"` + strings.Repeat("a", maxInputBytes-len(`{"finding":""}`)) + `"}`
	require.Len(t, padded, maxInputBytes)
	in, err := DecodeStrict(strings.NewReader(padded))
	require.NoError(t, err)
	assert.Len(t, in.Finding, maxInputBytes-len(`{"finding":""}`))
}
