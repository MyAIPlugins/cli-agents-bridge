package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// refuseArgs says what is wrong with the arguments of a call, in words about the
// caller's own input. The decoder's text is not used: it speaks the server's
// vocabulary (`Go struct field joinArgs.project of type string`), and an error's text
// is not something Refusef may carry (see ToolError). What the caller sent, its key
// and the kind of value, is read from the typed error and written again here.
//
// What it does not recognise is not the caller's doing (a tool decoding into a value
// it cannot, a decoder text that changed): it comes back as an ordinary error, which
// the server logs while the model is told the neutral text.
func refuseArgs(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Type != nil {
		return Refusef("invalid arguments: %s", typeMismatch(typeErr.Field, typeErr.Value, typeErr.Type.Kind()))
	}
	if key, ok := unknownField(err); ok {
		return Refusef("invalid arguments: unknown field %q", key)
	}
	return fmt.Errorf("decode the arguments: %w", err)
}

// unknownField reads the key out of the decoder's complaint about a key the tool does
// not have. The standard library has no typed error for it; its text is the one stable
// form (`json: unknown field "x"`), and the key in it is the caller's own, nothing of
// the machine. Any other text is not read.
func unknownField(err error) (string, bool) {
	rest, ok := strings.CutPrefix(err.Error(), "json: unknown field ")
	if !ok {
		return "", false
	}
	key, uerr := strconv.Unquote(rest)
	return key, uerr == nil
}

// typeMismatch is the sentence for a value of the wrong kind. `value` is what the
// decoder saw ("string", "number", "array"...; a number may carry its digits after a
// space, which are left out: they are no use to say what was wrong). An empty field
// means the arguments as a whole were not an object.
func typeMismatch(field, value string, want reflect.Kind) string {
	got, digits, _ := strings.Cut(value, " ")
	if field == "" {
		return "the arguments must be a JSON object, not " + jsonValue(got)
	}
	if got == "number" && digits != "" {
		return fmt.Sprintf("%q must be %s, got a number that does not fit", field, jsonKind(want))
	}
	return fmt.Sprintf("%q must be %s, got %s", field, jsonKind(want), jsonValue(got))
}

// jsonKind names what a Go kind is on the wire.
func jsonKind(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	default:
		return "a different kind of value"
	}
}

// jsonValue names the kind of value the decoder reports having found.
func jsonValue(word string) string {
	switch word {
	case "string":
		return "a string"
	case "number":
		return "a number"
	case "bool":
		return "a boolean"
	case "array":
		return "an array"
	case "object":
		return "an object"
	default:
		return "a value of another kind"
	}
}
