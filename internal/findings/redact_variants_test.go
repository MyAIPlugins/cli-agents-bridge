package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The variants a rule must not be fooled by (diff gate): the same secret glued to
// what comes before and after it. The JWT variants are in jwt_test.go. Every
// fixture is built at run time (D15).

// The branch next door: every rule, with its secret glued to a word character.
// `_` is a word character, so `\b` let `prefix_sk-...` through; no rule has a
// boundary on the left now, and a letter or a digit in front changes nothing.
func TestScan_EachRuleCatchesItsSecretAfterAWordCharacter(t *testing.T) {
	for rule, fixture := range secretFixtures() {
		for _, glued := range []string{"prefix_", "x", "7"} {
			t.Run(rule+"/after "+glued, func(t *testing.T) {
				got, hit := Scan(glued+fixture, nil)
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

// skKey is one form of an `sk-` key and the rule that names it.
type skKey struct{ name, key, rule string }

// skKeys are the forms the rules take: the four prefixes that say what they are,
// the legacy OpenAI shape (`sk-` and 48 letters and digits), and the marker rule
// for what the first two do not cover (a legacy key of another length,
// `sk-None-`, `sk-service-<name>-`). The body is made of pieces.
func skKeys() []skKey {
	rep := strings.Repeat
	return []skKey{
		{"sk-ant-", "sk-" + "ant-api03-" + rep("a", 30), "anthropic-key"},
		{"sk-proj-", "sk-" + "proj-" + rep("a", 30), "openai-key"},
		{"sk-svcacct-", "sk-" + "svcacct-" + rep("a", 30), "openai-key"},
		{"sk-admin-", "sk-" + "admin-" + rep("a", 30), "openai-key"},
		{"legacy, 48 letters and digits", "sk-" + rep("Ab3", 16), "openai-key"},
		{"legacy with the marker, 20+20", "sk-" + rep("a", 20) + openAIMarker + rep("b", 20), "openai-key"},
		{"legacy with the marker, 30+30 (longer than 48)", "sk-" + rep("a", 30) + openAIMarker + rep("b", 30), "openai-key"},
		{"legacy with the marker, 15+15 (shorter)", "sk-" + rep("a", 15) + openAIMarker + rep("b", 15), "openai-key-marker"},
		{"legacy with the marker, 5+3", "sk-" + rep("a", 5) + openAIMarker + rep("b", 3), "openai-key-marker"},
		{"sk-None- with the marker", "sk-" + "None-" + rep("a", 20) + openAIMarker + rep("b", 20), "openai-key-marker"},
		{"sk-service-x- with the marker", "sk-" + "service-x-" + rep("a", 20) + openAIMarker + rep("b", 20), "openai-key-marker"},
	}
}

// Whatever comes before the key, it is found: nothing, a letter, a digit, `_`,
// a word. The left context used to be a condition, and a key glued to a letter or
// a digit went through. Alone and inside a sentence.
func TestScan_AnSkKeyIsFoundWhateverComesBeforeIt(t *testing.T) {
	require.Len(t, strings.TrimPrefix(skKeys()[4].key, "sk-"), 48, "premise: the legacy fixture is 48 characters long")

	for _, k := range skKeys() {
		for _, before := range []string{"", "x", "7", "_", "prefix_", "token", "A9_"} {
			t.Run(k.name+"/after "+before, func(t *testing.T) {
				rule, hit := Scan(before+k.key, nil)
				require.True(t, hit)
				assert.Equal(t, k.rule, rule)

				rule, hit = Scan("see "+before+k.key+" for details", nil)
				require.True(t, hit)
				assert.Equal(t, k.rule, rule)
			})
		}
	}
}

// The legacy shape is exact on the left side of the count and open on the right:
// 47 letters and digits, or a dash in the middle, is not a key; 49 is one, because
// the first 48 are, and a key glued to what follows it is the case the AWS rule
// has no boundary for either.
func TestScan_TheLegacyShapeIsFortyEightLettersAndDigits(t *testing.T) {
	rep := strings.Repeat
	for name, value := range map[string]string{
		"47 letters and digits":     "sk-" + rep("Ab3", 15) + "Ab",
		"48 characters with a dash": "sk-" + rep("Ab3", 8) + "-" + rep("Ab3", 7) + "Ab",
		"48 characters with a _":    "sk-" + rep("Ab3", 8) + "_" + rep("Ab3", 7) + "Ab",
		"sk- and 20 letters":        "sk-" + rep("a", 20),
	} {
		t.Run(name, func(t *testing.T) {
			_, hit := Scan(value, nil)
			assert.False(t, hit, "%s is not a key of the legacy shape", name)
		})
	}

	forty9 := "sk-" + rep("Ab3", 16) + "Z"
	rule, hit := Scan(forty9, nil)
	require.True(t, hit, "49 letters and digits: the first 48 are a key")
	assert.Equal(t, "openai-key", rule)
}

// Why the generic `sk-` prefix is not a rule: a key is a kebab-case slug, and many
// English words end in "sk". Premise asserted: the body that follows is long
// enough to be taken for a key by a rule that asked only for 20 characters.
func TestScan_KebabCaseKeysThatContainSkAreNotKeys(t *testing.T) {
	for _, slug := range []string{
		"task-description-overflow-in-the-lock-registry",
		"disk-io-race-while-writing-the-registry",
		"risk-assessment-of-the-lock-race-on-windows",
		"sk-description-overflow-in-store",
		"sk-description-overflow-in-the-lock-registry-on-windows",
	} {
		t.Run(slug, func(t *testing.T) {
			body := slug[strings.Index(slug, "sk-"):]
			require.GreaterOrEqual(t, len(body), len("sk-")+20, "premise: the body is long enough to be a key")

			_, hit := Scan(slug, nil)
			assert.False(t, hit, "the slug as a whole is not a key")
			_, hit = Scan(" "+body, nil)
			assert.False(t, hit, "the body with a space before it is not a key either")
		})
	}
}

// The price of recognising the four prefixes anywhere, written down so that it is
// known and not found: a name that contains one of them followed by 20 more
// characters is refused, with the rule that owns the prefix. A refused row costs
// one rewrite. The prefixes are the ones the help names (PrefixedKeyPrefixes).
func TestScan_ANameThatContainsAPrefixedKeyPrefixIsRefused(t *testing.T) {
	require.Equal(t, []string{"sk-ant-", "sk-proj-", "sk-svcacct-", "sk-admin-"}, PrefixedKeyPrefixes())

	for _, k := range prefixedKeys {
		t.Run(k.prefix, func(t *testing.T) {
			name := "word-" + k.prefix + strings.Repeat("a", 20)
			rule, hit := Scan(name, nil)
			require.True(t, hit)
			assert.Equal(t, k.rule, rule)

			_, hit = Scan("word-"+k.prefix+strings.Repeat("a", 19), nil)
			assert.False(t, hit, "with fewer than 20 characters after the prefix it passes")
		})
	}

	slug := "task-admin-console-timeout-fix-on-windows"
	body := slug[strings.Index(slug, "sk-"):]
	require.GreaterOrEqual(t, len(strings.TrimPrefix(body, "sk-admin-")), 20, "premise: 20 characters follow the prefix")
	rule, hit := Scan(slug, nil)
	require.True(t, hit)
	assert.Equal(t, "openai-key", rule)
}

// The other known false positive, written down: the legacy OpenAI form has no
// boundary on the left, so `sk-`, the end of `disk-` or `task-`, followed by a
// digest of 48 letters and digits is refused (`/tmp/disk-<sha256>.log`). It is not
// a defect: a boundary would lose keys glued to a word. The digest is computed at
// run time. A shorter digest, such as a commit hash, is not affected.
func TestScan_APointerWithADigestAfterSkIsRefused(t *testing.T) {
	sum := sha256.Sum256([]byte("synthetic fixture"))
	digest := hex.EncodeToString(sum[:])
	require.Len(t, digest, 64, "premise: a sha256 in hex is 64 characters")

	for name, pointer := range map[string]string{
		"a log under disk-":      "/tmp/disk-" + digest + ".log",
		"a cache file of task-":  "cache/task-" + digest + ".bin",
		"exactly 48 characters":  "cache/task-" + digest[:48] + ".bin",
		"the digest in a phrase": "see /tmp/disk-" + digest + ".log for the output",
	} {
		t.Run(name, func(t *testing.T) {
			rule, hit := Scan(pointer, nil)
			require.True(t, hit)
			assert.Equal(t, "openai-key", rule)
		})
	}

	for name, pointer := range map[string]string{
		"47 characters":         "cache/task-" + digest[:47] + ".bin",
		"a commit hash (40)":    "cache/task-" + digest[:40] + ".bin",
		"a short hash":          "internal/findings/store.go:120 at 3519a9a",
		"a digest after disk_":  "/tmp/disk_" + digest + ".log",
		"a digest after a dash": "/tmp/build-" + digest + ".log",
	} {
		t.Run("passes: "+name, func(t *testing.T) {
			_, hit := Scan(pointer, nil)
			assert.False(t, hit, "%s must not be refused", name)
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
