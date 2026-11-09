package findings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The variants a rule must not be fooled by (diff gate, round 1): the same secret
// glued to what comes before and after it. The JWT variants are in jwt_test.go.
// Every fixture is built at run time (D15).

// The branch next door: every rule, with its secret glued to a word character.
// `_` is a word character, so `\b` let `prefix_sk-...` through; the rules with an
// unmistakable prefix have no boundary now, the JWT is found whatever is glued to
// it, and the two `sk-` rules are the declared exceptions for a letter or a digit.
func TestScan_EachRuleCatchesItsSecretAfterAWordCharacter(t *testing.T) {
	limits := map[string]bool{"anthropic-key": true, "openai-key": true}
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
