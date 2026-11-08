package findings

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The variants a rule must not be fooled by (diff gate, round 1): the same secret
// with another serialization, or glued to what comes before and after it.
// Every fixture is built at run time (D15).

const (
	claims       = `{"sub":"x1"}`
	headerPlain  = `{"alg":"HS256","typ":"JWT"}`
	signatureLen = 43
)

func encode(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// A JWT is valid however its JSON is serialized: empty claims, white space in the
// payload, white space or a newline at the start of the header, an unsecured
// token with an empty signature, a five-segment JWE-shaped run.
func TestJWT_ValidTokensAreRejectedWhateverTheirSerialization(t *testing.T) {
	sig := strings.Repeat("s", signatureLen)
	cases := map[string]string{
		"compact (the control)":      makeJWT(headerPlain, claims),
		"empty claims":               makeJWT(headerPlain, `{}`),
		"white space in the payload": makeJWT(headerPlain, `{ "sub": "x1" }`),
		"space before the header":    makeJWT(` `+headerPlain, claims),
		"white space in the header":  makeJWT(`{ "alg": "HS256", "typ": "JWT" }`, claims),
		"newline before the header":  makeJWT("\n"+headerPlain, claims),
		"tab and CRLF in the header": makeJWT("\t\r\n"+headerPlain, claims),
		"unsecured, empty signature": encode(`{"alg":"none"}`) + "." + encode(claims) + ".",
		"empty header object":        makeJWT(`{}`, claims),
		"five segments (JWE shape)":  encode(`{"alg":"dir","enc":"A128GCM"}`) + "." + sig + "." + sig + "." + sig + "." + sig,
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
	}
	for name, value := range passes {
		t.Run(name, func(t *testing.T) {
			_, hit := Scan(value, nil)
			assert.False(t, hit, "%s must not be taken for a token", name)
		})
	}
}

// The token can sit after a prefix made of base64url characters (`_` and `-`
// are in the alphabet, so a prefix like `token_` is indistinguishable from the
// first segment) or after other dotted segments.
func TestJWT_AfterAPrefixOrAnotherSegmentIsStillFound(t *testing.T) {
	token := makeJWT(headerPlain, claims)
	for _, glued := range []string{"prefix_", "my_api_token_", "bearer-", "a-b_", "a.b.", "x1.y2."} {
		t.Run(glued, func(t *testing.T) {
			rule, hit := Scan(glued+token, nil)
			require.True(t, hit)
			assert.Equal(t, "jwt", rule)
		})
	}
}

// Declared limit, with its premise asserted: the same token that is found after
// `_` is not found when a letter or a digit is glued to its first character,
// because then the first segment is a different base64 string; and it is not
// found after more separators than jwtMaxStarts, which bounds the work.
func TestJWT_TheLimitsOfWhereItIsLookedFor(t *testing.T) {
	token := makeJWT(headerPlain, claims)
	_, hit := Scan("_"+token, nil)
	require.True(t, hit, "premise: after a separator the token is found")

	for _, glued := range []string{"x", "7"} {
		_, hit := Scan(glued+token, nil)
		assert.False(t, hit, "a token glued to %q is a declared limit", glued)
	}

	within := strings.Repeat("a_", jwtMaxStarts-1) + token
	_, hit = Scan(within, nil)
	assert.True(t, hit, "the last start that is tried")
	beyond := strings.Repeat("a_", jwtMaxStarts) + token
	_, hit = Scan(beyond, nil)
	assert.False(t, hit, "one separator more than jwtMaxStarts is a declared limit")
}

// Many dots and separators cost a bounded amount and do not break the answer.
func TestJWT_HostileShapesTerminateWithTheRightAnswer(t *testing.T) {
	for name, value := range map[string]string{
		"dots only":              strings.Repeat(".", 5000),
		"one-letter segments":    strings.Repeat("a.", 5000),
		"separators then dots":   strings.Repeat("_-", 5000) + ".a.b",
		"a long segment":         strings.Repeat("e", jwtMaxHeaderChars+10) + ".b.c",
		"a long run of segments": strings.Repeat("ab.", 3000),
	} {
		t.Run(name, func(t *testing.T) {
			_, hit := Scan(value, nil)
			assert.False(t, hit)
		})
	}
}

// The branch next door: every rule, with its secret glued to a word character.
// `_` is a word character, so `\b` let `prefix_sk-...` through; the rules with an
// unmistakable prefix have no boundary now, and the two `sk-` rules and the JWT
// are the declared exceptions for a letter or a digit.
func TestScan_EachRuleCatchesItsSecretAfterAWordCharacter(t *testing.T) {
	limits := map[string]bool{"anthropic-key": true, "openai-key": true, "jwt": true}
	for rule, fixture := range secretFixtures() {
		for _, glued := range []string{"prefix_", "x", "7"} {
			t.Run(rule+"/after "+glued, func(t *testing.T) {
				got, hit := Scan(glued+fixture, nil)
				if limits[rule] && glued != "prefix_" {
					assert.False(t, hit, "%s glued to a letter or a digit is a declared limit", rule)
					return
				}
				require.True(t, hit)
				assert.Equal(t, rule, got)
			})
		}
	}
}

// The AWS key id is 20 characters; whatever follows it does not change that.
func TestScan_AnAwsKeyIdFollowedByAWordCharacterIsStillAKey(t *testing.T) {
	key := secretFixtures()["aws-access-key"]
	for _, after := range []string{"_PROD", "x", "XYZ", "9", "_"} {
		t.Run(after, func(t *testing.T) {
			rule, hit := Scan(key+after, nil)
			require.True(t, hit)
			assert.Equal(t, "aws-access-key", rule)
		})
	}
}

// Why the `sk-` rules keep a left context: a key is a kebab-case slug, and many
// English words end in "sk". Premise asserted: the body that follows is long
// enough to be a key when the prefix stands alone.
func TestScan_KebabCaseKeysThatContainSkAreNotKeys(t *testing.T) {
	for _, slug := range []string{
		"task-description-overflow-in-the-lock-registry",
		"disk-io-race-while-writing-the-registry",
		"risk-assessment-of-the-lock-race-on-windows",
	} {
		t.Run(slug, func(t *testing.T) {
			body := slug[strings.Index(slug, "sk-"):]
			require.GreaterOrEqual(t, len(body), len("sk-")+20, "premise: the body is long enough to be a key")
			_, hit := Scan(" "+body, nil)
			require.True(t, hit, "premise: the same body with a space before it is taken for a key")

			_, hit = Scan(slug, nil)
			assert.False(t, hit, "the slug as a whole is not a key")
		})
	}
}

// The URL rule has no boundary on the left either: a scheme glued to a word
// character is still a URL.
func TestScan_AUrlGluedToAWordCharacterIsStillAUrl(t *testing.T) {
	for _, value := range []string{"prefix_https://example.com/x", "xhttps://example.com", "7ftp://host"} {
		rule, hit := Scan(value, nil)
		require.True(t, hit, value)
		assert.Equal(t, "url", rule, value)
	}
}
