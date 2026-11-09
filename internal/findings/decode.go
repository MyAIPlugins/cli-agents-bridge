package findings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// maxInputBytes bounds what `add` reads from stdin. The columns together stay
// under 3 KiB; the rest is slack, and the limit keeps a stray pipe from filling memory.
const maxInputBytes = 64 << 10

// unknownFieldPrefix is how encoding/json words a field DisallowUnknownFields
// refused. The error has no type, so the prefix is the only way to tell it
// apart; the rest of the message (the received key) is never used.
const unknownFieldPrefix = "json: unknown field "

var numberType = reflect.TypeOf(json.Number(""))

// DecodeStrict reads the single JSON object `add` takes on stdin. Unknown keys
// are refused, and so are ts and first_seen (the command writes them).
//
// Every error is a fixed text. encoding/json puts pieces of the input in its
// messages (a bad number comes back as `cannot unmarshal string "abc"...`), and
// the input may be exactly the secret that is about to be refused, so those
// messages are translated and never forwarded.
func DecodeStrict(r io.Reader) (Input, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return Input{}, fmt.Errorf("findings: reading stdin: %w", err)
	}
	if len(data) > maxInputBytes {
		return Input{}, decodeFailure("input is larger than %d bytes", maxInputBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in Input
	if err := dec.Decode(&in); err != nil {
		return Input{}, translateDecodeError(err, dec.InputOffset())
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Input{}, decodeFailure("more than one JSON value on stdin")
	}
	if in.Ts != nil {
		return Input{}, decodeFailure("column ts %s", reasonCommandOwns)
	}
	if in.FirstSeen != nil {
		return Input{}, decodeFailure("column first_seen %s", reasonCommandOwns)
	}
	return in, nil
}

func decodeFailure(format string, args ...any) error {
	return fmt.Errorf("findings: "+format, args...)
}

func translateDecodeError(err error, offset int64) error {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return decodeFailure("input is empty")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return decodeFailure("stdin is not valid JSON (byte offset %d)", offset)
	case errors.As(err, &syntax):
		return decodeFailure("stdin is not valid JSON (byte offset %d)", syntax.Offset)
	case errors.As(err, &typeErr):
		return typeFailure(typeErr)
	case strings.HasPrefix(err.Error(), unknownFieldPrefix):
		return decodeFailure("unknown column; the columns are: %s", strings.Join(inputColumnNames(), ", "))
	default:
		return decodeFailure("stdin is not a valid findings object")
	}
}

// typeFailure names the column from the Go struct (a fixed set), never from the input.
func typeFailure(e *json.UnmarshalTypeError) error {
	switch {
	case e.Type == numberType:
		// encoding/json does not say which of the two number columns it was.
		return decodeFailure("columns token and costo must be a number or a string holding a number")
	case isInputColumn(e.Field):
		return decodeFailure("column %s must be a string", e.Field)
	case e.Field == "":
		return decodeFailure("stdin must be one JSON object")
	default:
		return decodeFailure("a column has the wrong type")
	}
}

func inputColumnNames() []string {
	names := make([]string, len(inputColumns))
	for i, c := range inputColumns {
		names[i] = c.name
	}
	return names
}

func isInputColumn(name string) bool {
	return contains(inputColumnNames(), name)
}
