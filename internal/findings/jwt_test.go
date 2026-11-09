package findings

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The JWT rule decides by structure (jwt.go): a header that is a JSON object with
// a string `alg`, followed by the segments its shape needs. Every fixture is built
// at run time (D15).

const (
	claims       = `{"sub":"x1"}`
	headerPlain  = `{"alg":"HS256","typ":"JWT"}`
	headerJWE    = `{"alg":"dir","enc":"A128GCM"}`
	signatureLen = 43
)

func encode(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// columnMaxBytes is the length cap of an input column, read from the spec so that
// a test that says "the most the column allows" follows the column.
func columnMaxBytes(t *testing.T, name string) int {
	t.Helper()
	for _, c := range inputColumns {
		if c.name == name {
			require.Positive(t, c.maxBytes, "column %s has a cap", name)
			return c.maxBytes
		}
	}
	require.Failf(t, "no such column", name)
	return 0
}

// makeJWEDir builds a compact JWE with direct encryption, as a library does: the
// encrypted key is empty (RFC 7516 5.1 step 5), the content is encrypted with
// AES-GCM and the protected header, as it is written in the token, is the
// additional authenticated data. The token is opened again before it is returned,
// so the fixture is a token that decrypts and not a string that looks like one.
func makeJWEDir(t *testing.T, header, plaintext string) string {
	t.Helper()
	key := bytes.Repeat([]byte{0x2a}, 16)
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	iv := bytes.Repeat([]byte{0x07}, gcm.NonceSize())

	protected := encode(header)
	sealed := gcm.Seal(nil, iv, []byte(plaintext), []byte(protected))
	tagAt := len(sealed) - gcm.Overhead()
	enc := base64.RawURLEncoding.EncodeToString
	token := strings.Join([]string{protected, "", enc(iv), enc(sealed[:tagAt]), enc(sealed[tagAt:])}, ".")

	parts := strings.Split(token, ".")
	require.Len(t, parts, 5, "premise: five segments")
	require.Empty(t, parts[1], "premise: the encrypted key is empty")
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[3])
	require.NoError(t, err)
	tag, err := base64.RawURLEncoding.DecodeString(parts[4])
	require.NoError(t, err)
	opened, err := gcm.Open(nil, iv, append(ciphertext, tag...), []byte(parts[0]))
	require.NoError(t, err, "premise: the token decrypts")
	require.Equal(t, plaintext, string(opened))
	return token
}

// A JWT is valid however its JSON is serialized: empty claims, white space in the
// payload, white space or a newline at the start of the header, an unsecured
// token with an empty signature, a JWE with direct encryption.
func TestJWT_ValidTokensAreRejectedWhateverTheirSerialization(t *testing.T) {
	sig := strings.Repeat("s", signatureLen)
	cases := map[string]string{
		"compact (the control)":            makeJWT(headerPlain, claims),
		"empty claims":                     makeJWT(headerPlain, `{}`),
		"white space in the payload":       makeJWT(headerPlain, `{ "sub": "x1" }`),
		"space before the header":          makeJWT(` `+headerPlain, claims),
		"white space in the header":        makeJWT(`{ "alg": "HS256", "typ": "JWT" }`, claims),
		"newline before the header":        makeJWT("\n"+headerPlain, claims),
		"tab before the header":            makeJWT("\t"+headerPlain, claims),
		"carriage return before":           makeJWT("\r"+headerPlain, claims),
		"tab and CRLF in the header":       makeJWT("\t\r\n"+headerPlain, claims),
		"unsecured, empty signature":       encode(`{"alg":"none"}`) + "." + encode(claims) + ".",
		"the name of alg in capitals":      makeJWT(`{"ALG":"HS256"}`, claims),
		"alg after other parameters":       makeJWT(`{"typ":"JWT","kid":"k1","alg":"HS256"}`, claims),
		"a JWS with an enc of its own":     makeJWT(`{"alg":"HS256","enc":"A128GCM"}`, claims),
		"five segments, all with content":  encode(headerJWE) + "." + sig + "." + sig + "." + sig + "." + sig,
		"JWE, direct, the key is empty":    makeJWEDir(t, headerJWE, claims),
		"JWE, direct, alg in capitals too": makeJWEDir(t, `{"ALG":"dir","ENC":"A128GCM"}`, claims),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			rule, hit := Scan(token, nil)
			require.True(t, hit)
			assert.Equal(t, "jwt", rule)

			rule, hit = Scan("see "+token+" for details", nil)
			require.True(t, hit)
			assert.Equal(t, "jwt", rule)
		})
	}
}

// What is not a token passes. Each case is the neighbour of a rejected one, so
// the table shows what the structural check is looking at.
func TestJWT_WhatIsNotATokenPasses(t *testing.T) {
	sig := strings.Repeat("s", signatureLen)
	require.True(t, containsJWT(makeJWT(headerPlain, claims)), "premise: the same shape with a real header is a token")

	passes := map[string]string{
		"a version":                         "1.22.3",
		"a pre-release":                     "v1.22.3-rc.1",
		"three words":                       "alpha.beta.gamma",
		"a file name":                       "file.name.go",
		"a host name":                       "build.example.org",
		"a header that is an array":         encode(`["alg","HS256"]`) + "." + encode(claims) + "." + sig,
		"a header that is a string":         encode(`"alg"`) + "." + encode(claims) + "." + sig,
		"a header that is not JSON":         encode(`hello`) + "." + encode(claims) + "." + sig,
		"a header that is cut":              encode(`{"alg":"HS256"`) + "." + encode(claims) + "." + sig,
		"only two segments":                 encode(headerPlain) + "." + encode(claims),
		"an empty payload segment":          encode(headerPlain) + ".." + sig,
		"a header that is only white space": encode(" \n ") + "." + encode(claims) + "." + sig,
		"an empty header object":            makeJWT(`{}`, claims),
		"a header without alg":              makeJWT(`{"typ":"JWT"}`, claims),
		"an alg that is a number":           makeJWT(`{"alg":256}`, claims),
		"an alg that is null":               makeJWT(`{"alg":null}`, claims),
		"an alg that is a list":             makeJWT(`{"alg":["HS256"]}`, claims),
		"enc without alg, five segments":    encode(`{"enc":"A128GCM"}`) + "." + sig + "." + sig + "." + sig + "." + sig,
		"JWE shape cut to four segments":    encode(headerJWE) + ".." + sig + "." + sig,
	}
	for name, value := range passes {
		t.Run(name, func(t *testing.T) {
			_, hit := Scan(value, nil)
			assert.False(t, hit, "%s must not be taken for a token", name)
		})
	}
}

// The nine names that the first rule took for a token because `e30` is the base64
// of `{}`, and the neighbours that never were. They are text a finding says: a
// file, a model, a release. `{}` is not a header, so none is a token.
func TestJWT_NamesThatContainTheBase64OfAnEmptyObjectPass(t *testing.T) {
	for _, value := range []string{
		"file_e30.tar.gz", "docs/model-e30.spec.md:12", "bmw-e30.repair.manual", "release_e30.0.1",
		"build-e30.log.1", "e30.a.b", "IHt9.x.y", "e30K.x.y", "e30.example.com",
		"v1.2.3", "a.b.c", "store.go.orig.bak", "e30.txt", "page-e30.html",
	} {
		t.Run(value, func(t *testing.T) {
			rule, hit := Scan(value, nil)
			assert.False(t, hit, "%q was refused as %s", value, rule)
		})
	}
	require.Equal(t, `{}`, string(mustDecode(t, "e30")), "premise: e30 is an empty object")
	require.Equal(t, ` {}`, string(mustDecode(t, "IHt9")), "premise: IHt9 is an empty object after a space")
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(s)
	require.NoError(t, err)
	return raw
}

// A direct JWE is found alone, in a sentence, and after a prefix; and a JWE whose
// encrypted key is not empty is found too (the case that was always covered).
func TestJWT_TheEncryptedKeyMayBeEmptyOrNot(t *testing.T) {
	sig := strings.Repeat("s", signatureLen)
	dir := makeJWEDir(t, headerJWE, claims)
	withKey := encode(`{"alg":"RSA-OAEP","enc":"A128GCM"}`) + "." + sig + "." + sig + "." + sig + "." + sig

	for name, value := range map[string]string{
		"alone":                  dir,
		"in a sentence":          "the header said " + dir + " and nothing else",
		"after a prefix":         "token_" + dir,
		"after a letter":         "x" + dir,
		"with an encrypted key":  withKey,
		"after other segments":   "a.b." + dir,
		"inside quotes and text": `"jwe": "` + dir + `",`,
	} {
		t.Run(name, func(t *testing.T) {
			rule, hit := Scan(value, nil)
			require.True(t, hit)
			assert.Equal(t, "jwt", rule)
		})
	}
}

// The token can sit after a prefix made of base64url characters (`_` and `-`
// are in the alphabet, so a prefix like `token_` is indistinguishable from the
// first segment), after a letter or a digit, or after other dotted segments.
func TestJWT_AfterAPrefixOrAnotherSegmentIsStillFound(t *testing.T) {
	token := makeJWT(headerPlain, claims)
	for _, glued := range []string{"prefix_", "my_api_token_", "bearer-", "a-b_", "a.b.", "x1.y2.", "x", "7", "token", "Bearer7"} {
		t.Run(glued, func(t *testing.T) {
			rule, hit := Scan(glued+token, nil)
			require.True(t, hit)
			assert.Equal(t, "jwt", rule)
		})
	}
}

// No count of starts accepts: a header is found after 16 prefixes, after 17, and
// after as many as the column allows. The old rule looked at 16 places in a
// segment and let the rest through; the answer cannot depend on how long the
// prefix is.
func TestJWT_NoLengthOfPrefixLetsATokenThrough(t *testing.T) {
	token := makeJWT(headerPlain, claims)
	max := (columnMaxBytes(t, "finding") - len(token)) / 2
	require.Greater(t, max, 17, "premise: the column allows more than 17 prefixes")

	for _, n := range []int{1, 15, 16, 17, 18, 100, max} {
		for _, unit := range []string{"a_", "a-", "ab"} {
			value := strings.Repeat(unit, n) + token
			require.LessOrEqual(t, len(value), columnMaxBytes(t, "finding"), "premise: the value fits the column")

			rule, hit := Scan(value, nil)
			assert.True(t, hit, "%d x %q before the token", n, unit)
			assert.Equal(t, "jwt", rule, "%d x %q before the token", n, unit)
		}
	}
}

// Many dots, separators and long segments cost a bounded amount and do not break
// the answer: none of these holds a header, so none is refused.
func TestJWT_HostileShapesTerminateWithTheRightAnswer(t *testing.T) {
	for name, value := range map[string]string{
		"dots only":              strings.Repeat(".", 5000),
		"one-letter segments":    strings.Repeat("a.", 5000),
		"separators then dots":   strings.Repeat("_-", 5000) + ".a.b",
		"a long segment":         strings.Repeat("e", 9000) + ".b.c",
		"a long run of segments": strings.Repeat("ab.", 3000),
		"starts that all stop":   strings.Repeat("I", 100) + ".a.b",
	} {
		t.Run(name, func(t *testing.T) {
			_, hit := Scan(value, nil)
			assert.False(t, hit)
		})
	}
}

// The work limit refuses; it never accepts. `I` is the base64 of a space, so in a
// run of them every place is a start that has to be decoded (and none is a header:
// a string of white space is not a JSON object); there are enough of them to pass
// jwtMaxWork. The answer is "jwt", the rule that could not rule the run out. The
// same shape, short enough to be examined to the end, is not refused: that is what
// shows the limit is the cause.
func TestJWT_WhenTheWorkRunsOutTheRunIsRefused(t *testing.T) {
	require.True(t, mayStartJSONObject(strings.Repeat("I", 4)), "premise: every place in the run is a start")

	short := strings.Repeat("I", 100) + ".a.b"
	_, hit := Scan(short, nil)
	require.False(t, hit, "premise: examined to the end, there is no header")

	long := strings.Repeat("I", 5000) + ".a.b"
	rule, hit := Scan(long, nil)
	require.True(t, hit, "a run that cannot be ruled out is refused")
	assert.Equal(t, "jwt", rule)

	// The longest value a column accepts is the worst case that is still examined:
	// about half a million characters of work, half the limit.
	max := columnMaxBytes(t, "finding")
	atTheCap := strings.Repeat("I", max-len(".a.b")) + ".a.b"
	require.Len(t, atTheCap, max)
	_, hit = Scan(atTheCap, nil)
	assert.False(t, hit, "a value at the column cap is examined to the end, not refused for the work")
}

// makeJWT builds a compact token at run time from a header and a payload JSON
// text, so that no whole token ever sits in the source (D15) and so that the
// serialization of the JSON, white space included, is the test's choice. The
// first fixture of this file was a string shaped like the old regular
// expression that decoded to garbage: it passed because the rule asked for that
// shape, which is exactly what let valid tokens through.
func makeJWT(header, payload string) string {
	return encode(header) + "." + encode(payload) + "." + strings.Repeat("s", signatureLen)
}
