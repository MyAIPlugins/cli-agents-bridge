package shellarg

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evalInShell asks a REAL /bin/sh what the rendered word becomes, and reports
// the resulting argv NUL-separated.
//
// Counting argv, not lines: a value containing a newline yields one argument and
// two lines, so a line count would call a correct quoting broken — which is the
// mistake the first version of this lot's test plan would have made.
func evalInShell(t *testing.T, rendered string) []string {
	t.Helper()
	// printf writes each argument followed by NUL; nothing else touches the data.
	out, err := exec.Command("/bin/sh", "-c", `printf '%s\0' `+rendered).Output()
	require.NoError(t, err, "the shell refused to evaluate %s", rendered)
	s := string(out)
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\x00"), "\x00")
}

// TestQuote_IsOneArgvAfterTheShell is the contract, checked where the contract
// lives — after a shell has had the string.
//
// AND AFTER A JSON ROUND-TRIP, which is the part that is easy to skip: the value
// travels inside `next`'s JSON page, so a test that renders and evaluates
// without encoding first is verifying the shell escaping while the JSON escaping
// — the other layer that can eat a backslash — never runs.
func TestQuote_IsOneArgvAfterTheShell(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, raw string }{
		{"already safe", "VAL-payload@alancurtisagency-payload"},
		{"a path", "/Users/alan/develop/thing"},
		{"a space", "VAL-x@/tmp/my repo"},
		{"an APOSTROPHE", "VAL-x@/Users/alan/Alan's Project"},
		{"a double quote", `VAL-x@/tmp/say "hi"`},
		{"command substitution", "VAL-x@/tmp/$(touch pwned)"},
		{"backticks", "VAL-x@/tmp/`touch pwned`"},
		{"a semicolon starting a second command", "VAL-x@/tmp/a;touch pwned"},
		{"a glob", "VAL-x@/tmp/*"},
		{"a backslash", `VAL-x@/tmp/back\slash`},
		{"a dollar", "VAL-x@/tmp/$HOME"},
		{"a pipe into another command", "VAL-x@/tmp/a|touch pwned"},
		{"every troublesome byte at once", `a b'c"d$e()f;g|h&i*j?k[l]m{n}o~p#q!r\s`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Through JSON first, exactly as the value reaches a reader.
			encoded, err := json.Marshal(map[string]string{"fromAddressShellArg": Quote(tc.raw)})
			require.NoError(t, err)
			var decoded map[string]string
			require.NoError(t, json.Unmarshal(encoded, &decoded))

			argv := evalInShell(t, decoded["fromAddressShellArg"])

			require.Len(t, argv, 1, "must be ONE argv entry, got %q from %q", argv, decoded["fromAddressShellArg"])
			assert.Equal(t, tc.raw, argv[0], "and it must be the original value, byte for byte")
		})
	}
}

// The ordinary case pays nothing: a token already inside the safe set comes back
// untouched, so tables and messages read exactly as they do today.
func TestQuote_LeavesOrdinaryTokensAlone(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"VAL-bridge",
		"VAL-payload@alancurtisagency-payload",
		"/Users/alan/develop/cli-agents-bridge",
		"esc-v08", "a.b-c_d", "1.2.3",
	} {
		assert.Equal(t, s, Quote(s), "%q needs no quoting and must not get any", s)
	}
}

// TestQuote_NoSideEffects is the half a round-trip cannot show: a value that
// LOOKS like a command must not run as one.
//
// Round-tripping proves the argv came back identical; it does not prove nothing
// else happened on the way. Here the payload would create a file, and the test
// asserts the file is absent — the difference between "the string survived" and
// "the string was inert".
func TestQuote_NoSideEffects(t *testing.T) {
	t.Parallel()
	marker := t.TempDir() + "/pwned"

	for _, raw := range []string{
		"VAL-x@/tmp/$(touch " + marker + ")",
		"VAL-x@/tmp/`touch " + marker + "`",
		"VAL-x@/tmp/a;touch " + marker,
	} {
		argv := evalInShell(t, Quote(raw))
		require.Len(t, argv, 1, "%q", raw)
		assert.Equal(t, raw, argv[0])

		_, err := exec.Command("/bin/sh", "-c", "test -e "+marker).Output()
		assert.Error(t, err,
			"%q must be DATA: if the marker exists, the shell executed the payload", raw)
	}
}

// Tab and newline are OUT OF CONTRACT for display, and this test states what is
// actually true of them instead of leaving the limit to be discovered: the VALUE
// survives the shell intact, and the RENDERING spans lines. Both halves are
// asserted, so nobody has to guess which one the comment meant.
func TestQuote_TabAndNewlineRoundTripButBreakTheLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw string }{
		{"tab", "VAL-x@/tmp/a\tb"},
		{"newline", "VAL-x@/tmp/a\nb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := Quote(tc.raw)
			argv := evalInShell(t, rendered)
			require.Len(t, argv, 1, "the VALUE is still one argument")
			assert.Equal(t, tc.raw, argv[0], "and byte-identical")

			if strings.Contains(tc.raw, "\n") {
				assert.Contains(t, rendered, "\n",
					"but the RENDERING spans lines — which is why a newline is out of contract "+
						"for any surface printed as rows, even though the value itself is fine")
			}
		})
	}
}

// The empty string is a value like any other, and the one most likely to be
// dropped: unquoted it disappears from argv entirely, turning "no scope" into
// "one fewer argument" at the far end.
func TestQuote_EmptyStringStaysAnArgument(t *testing.T) {
	t.Parallel()
	argv := evalInShell(t, Quote(""))
	require.Len(t, argv, 1, "an empty value must still be ONE argument, not none")
	assert.Equal(t, "", argv[0])
}
