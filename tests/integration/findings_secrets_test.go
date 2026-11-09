package integration

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strconv"
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

// fakeJWEDir is a compact JWE with direct encryption, as a library writes it: the
// encrypted key is empty and the content is AES-GCM with the protected header as
// additional authenticated data. It is opened again before it is returned, so the
// fixture is a token that decrypts and not a string that looks like one.
func fakeJWEDir(t *testing.T) string {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{0x2a}, 16))
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	iv := bytes.Repeat([]byte{0x07}, gcm.NonceSize())

	enc := base64.RawURLEncoding.EncodeToString
	protected := enc([]byte(`{"alg":"dir","enc":"A128GCM"}`))
	sealed := gcm.Seal(nil, iv, []byte(`{"sub":"x1"}`), []byte(protected))
	tagAt := len(sealed) - gcm.Overhead()
	token := strings.Join([]string{protected, "", enc(iv), enc(sealed[:tagAt]), enc(sealed[tagAt:])}, ".")

	opened, err := gcm.Open(nil, iv, sealed, []byte(protected))
	require.NoError(t, err, "premise: the token decrypts")
	require.Equal(t, `{"sub":"x1"}`, string(opened))
	require.Equal(t, 5, len(strings.Split(token, ".")), "premise: five segments, the second empty")
	require.Contains(t, token, protected+"..", "premise: the encrypted key is empty")
	return token
}

// findingColumnCap asks the binary how long a finding may be, by sending one that
// is too long and reading the number out of the refusal: the test follows the
// column and does not copy its limit.
func findingColumnCap(t *testing.T, r *findingsRepo) int {
	t.Helper()
	_, stderr, exit := r.run(t, findingJSON(t, map[string]string{"finding": strings.Repeat("x", 5000)}), "findings", "add")
	require.Equal(t, 1, exit, stderr)
	m := regexp.MustCompile(`column finding: is longer than (\d+) bytes`).FindStringSubmatch(stderr)
	require.Len(t, m, 2, "premise: the refusal says the cap: %q", stderr)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
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
	r := newFindingsRepo(t)
	jwt := fakeJWT(header, `{"sub":"x1"}`)
	jwe := fakeJWEDir(t)
	columnCap := findingColumnCap(t, r)
	maxPrefixes := (columnCap - len(jwt)) / 2

	cases := []struct{ name, rule, value string }{
		{"jwt, compact (the control)", "jwt", fakeJWT(header, `{"sub":"x1"}`)},
		{"jwt, direct JWE, alone", "jwt", jwe},
		{"jwt, direct JWE, in a sentence", "jwt", "pasted below: " + jwe + " (end of paste)"},
		{"jwt, after a letter", "jwt", "x" + jwt},
		{"jwt, after a digit", "jwt", "7" + jwt},
		{"jwt, after 15 prefixes", "jwt", strings.Repeat("a_", 15) + jwt},
		{"jwt, after 16 prefixes", "jwt", strings.Repeat("a_", 16) + jwt},
		{"jwt, after 17 prefixes", "jwt", strings.Repeat("a_", 17) + jwt},
		{fmt.Sprintf("jwt, after %d prefixes (the most the column allows)", maxPrefixes), "jwt", strings.Repeat("a_", maxPrefixes) + jwt},
		{"jwt, empty claims", "jwt", fakeJWT(header, `{}`)},
		{"jwt, space in the payload", "jwt", fakeJWT(header, `{ "sub": "x1" }`)},
		{"jwt, space in the header", "jwt", fakeJWT(` `+header, `{"sub":"x1"}`)},
		{"jwt, after a prefix", "jwt", "prefix_" + fakeJWT(header, `{"sub":"x1"}`)},
		{"an OpenAI-shaped key after a prefix", "openai-key", "prefix_" + "sk-" + "proj-" + strings.Repeat("Qz4", 11)},
		{"a GitHub token after a prefix", "github-token", "prefix_" + "gh" + "p_" + strings.Repeat("Wx7", 13)},
		{"an AWS key id before a suffix", "aws-access-key", "AK" + "IA" + strings.Repeat("Q7", 8) + "_PROD"},
		{"a URL after a prefix", "url", "prefix_" + "https://" + "internal.example.net/path"},
	}

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
// finding that only mentions the kind of secret, names that contain the base64 of
// an empty object (`e30`) and were taken for tokens, and slugs that contain `sk-`.
// Each is its own row, so a refusal of one is not hidden by the others.
func TestFindings_RealBinaryStillTakesFindingsThatOnlyMentionASecret(t *testing.T) {
	r := newFindingsRepo(t)
	var rows []map[string]string
	rows = append(rows,
		map[string]string{"finding": "the JWT in the log was accepted by the staging API", "key": "jwt-in-log"},
		map[string]string{"finding": "an OpenAI key shape was not recognised after a prefix", "key": "key-shape-after-prefix"},
	)
	for i, mention := range []string{
		"file_e30.tar.gz", "docs/model-e30.spec.md:12", "bmw-e30.repair.manual", "release_e30.0.1",
		"build-e30.log.1", "e30.a.b", "IHt9.x.y", "e30K.x.y", "e30.example.com",
		"v1.2.3", "a.b.c", "store.go.orig.bak", "e30.txt", "page-e30.html",
	} {
		rows = append(rows, map[string]string{"finding": "the name " + mention + " is in the log", "key": "mention-" + strconv.Itoa(i)})
	}
	for _, override := range rows {
		_, stderr, exit := r.run(t, findingJSON(t, override), "findings", "add")
		require.Equal(t, 0, exit, "%v: %s", override, stderr)
	}
}
