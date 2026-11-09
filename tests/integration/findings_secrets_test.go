package integration

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fragmentWindow is how many consecutive characters of a secret must not appear
// in anything the command prints.
const fragmentWindow = 8

func fakeJWT(header, payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(header)) + "." + enc([]byte(payload)) + "." + strings.Repeat("Zq9", 15)
}

func leaksFragment(output, secret string) bool {
	for i := 0; i+fragmentWindow <= len(secret); i++ {
		if strings.Contains(output, secret[i:i+fragmentWindow]) {
			return true
		}
	}
	return false
}

// Diff gate, round 1: the secrets the filter declares it refuses, in the shapes
// that used to get through. The unit tests show the rule; this shows the command
// with the real binary: exit 1, the column and the rule named, nothing of the
// value in stdout or stderr, and the registry byte for byte as it was.
// Every secret is built from pieces at run time.
func TestFindings_RealBinaryRefusesTheVariantsOfAKnownSecret(t *testing.T) {
	const header = `{"alg":"HS256","typ":"JWT"}`
	cases := []struct{ name, rule, value string }{
		{"jwt, compact (the control)", "jwt", fakeJWT(header, `{"sub":"x1"}`)},
		{"jwt, empty claims", "jwt", fakeJWT(header, `{}`)},
		{"jwt, space in the payload", "jwt", fakeJWT(header, `{ "sub": "x1" }`)},
		{"jwt, space in the header", "jwt", fakeJWT(` `+header, `{"sub":"x1"}`)},
		{"jwt, after a prefix", "jwt", "prefix_" + fakeJWT(header, `{"sub":"x1"}`)},
		{"an OpenAI-shaped key after a prefix", "openai-key", "prefix_" + "sk-" + "proj-" + strings.Repeat("Qz4", 11)},
		{"a GitHub token after a prefix", "github-token", "prefix_" + "gh" + "p_" + strings.Repeat("Wx7", 13)},
		{"an AWS key id before a suffix", "aws-access-key", "AK" + "IA" + strings.Repeat("Q7", 8) + "_PROD"},
		{"a URL after a prefix", "url", "prefix_" + "https://" + "internal.example.net/path"},
	}

	r := newFindingsRepo(t)
	_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
	require.Equal(t, 0, exit, stderr)
	before, err := os.ReadFile(r.registry())
	require.NoError(t, err)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.GreaterOrEqual(t, len(tc.value), fragmentWindow, "premise: the secret is long enough to have fragments")
			stdout, stderr, exit := r.run(t, findingJSON(t, map[string]string{"finding": tc.value}), "findings", "add")

			require.Equal(t, 1, exit, stderr)
			assert.Contains(t, stderr, "findings: column finding: contains something that looks like "+tc.rule+"\n")
			assert.Contains(t, stderr, "findings: nothing was written")
			assert.Empty(t, stdout)
			assert.False(t, leaksFragment(stdout+stderr, tc.value), "no %d-character piece of the value may be printed", fragmentWindow)

			after, err := os.ReadFile(r.registry())
			require.NoError(t, err)
			assert.Equal(t, before, after, "the registry is byte for byte as it was")
		})
	}
}

// The control that makes the test above mean something: the same command takes a
// finding that only mentions the kind of secret, and a slug that contains `sk-`.
func TestFindings_RealBinaryStillTakesFindingsThatOnlyMentionASecret(t *testing.T) {
	r := newFindingsRepo(t)
	for _, override := range []map[string]string{
		{"finding": "the JWT in the log was accepted by the staging API", "key": "jwt-in-log"},
		{"finding": "an OpenAI key shape was not recognised after a prefix", "key": "task-description-overflow-in-the-lock-registry"},
	} {
		_, stderr, exit := r.run(t, findingJSON(t, override), "findings", "add")
		require.Equal(t, 0, exit, "%v: %s", override, stderr)
	}
}
