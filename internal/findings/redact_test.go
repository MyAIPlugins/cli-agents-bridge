package findings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secretFixtures builds one fake secret per rule from pieces (D15): no whole
// secret-shaped string ever sits in the source, so the repository's own secret
// scanners and GitHub push protection have nothing to flag.
func secretFixtures() map[string]string {
	rep := strings.Repeat
	return map[string]string{
		"aws-access-key":            "AK" + "IA" + rep("A", 16),
		"aws-secret-key":            "aws_secret_" + "access_key=" + rep("a", 40),
		"github-fine-grained-token": "github_" + "pat_" + rep("a", 22) + "_" + rep("b", 59),
		"github-token":              "gh" + "p_" + rep("a", 36),
		"anthropic-key":             "sk-" + "ant-api03-" + rep("a", 30),
		"openai-key":                "sk-" + "proj-" + rep("a", 30),
		"openai-key-marker":         "sk-" + "None-" + rep("a", 20) + openAIMarker + rep("b", 20),
		"slack-token":               "xo" + "xb-" + rep("1", 12),
		"pem-private-key":           "-----BEGIN " + "OPENSSH PRIVATE KEY-----",
		"jwt":                       makeJWT(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"x1"}`),
		"secret-assignment":         "pass" + "word=" + "hunter22",
		"url":                       "https://example.com/x",
		"email":                     "a.b@example.org",
	}
}

// openAIMarker is the string every OpenAI key of the forms known today carries in
// the middle. It is built from pieces like the keys (D15).
const openAIMarker = "T3Blbk" + "FJ"

func TestScan_EachSecretTypeIsRejected(t *testing.T) {
	for rule, value := range secretFixtures() {
		t.Run(rule, func(t *testing.T) {
			got, hit := Scan(value, nil)
			require.True(t, hit, "the fixture for %s must be rejected", rule)
			assert.Equal(t, rule, got)

			// The same value inside a sentence: the rule is not anchored to the whole column.
			got, hit = Scan("see "+value+" for details", nil)
			require.True(t, hit)
			assert.Equal(t, rule, got)
		})
	}
}

func TestScan_RuleNamesAreUniqueAndTheListIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range contentRules {
		assert.NotNil(t, r.re, r.name)
		assert.False(t, seen[r.name], "duplicate rule name %s", r.name)
		seen[r.name] = true
	}
	assert.Len(t, contentRules, 13, "13 regular-expression rules, the 14th (person-name) is a list")
	for rule := range secretFixtures() {
		assert.True(t, seen[rule], "no rule named %s", rule)
	}
	// An Anthropic key is also shaped like an OpenAI one; the order decides which name is reported.
	got, _ := Scan(secretFixtures()["anthropic-key"], nil)
	assert.Equal(t, "anthropic-key", got)
}

// The help names the rules from ContentRuleNames. The list has to be the one Scan
// really applies: every name Scan reports is listed, every listed name is
// reported by some input, and the person-name rule (a list, not a pattern) is last.
func TestContentRuleNames_AreTheRulesScanReports(t *testing.T) {
	names := ContentRuleNames()
	require.Len(t, names, 14)
	assert.Equal(t, personNameRule, names[len(names)-1])

	reported := map[string]bool{}
	for rule, value := range secretFixtures() {
		got, hit := Scan(value, nil)
		require.True(t, hit, rule)
		reported[got] = true
	}
	got, hit := Scan("Marta wrote this", []string{"Marta"})
	require.True(t, hit)
	reported[got] = true

	listed := map[string]bool{}
	for _, n := range names {
		listed[n] = true
		assert.True(t, reported[n], "listed rule %s is never reported by Scan", n)
	}
	for n := range reported {
		assert.True(t, listed[n], "Scan reports %s but the help does not list it", n)
	}
}

func TestScan_NearMissesPass(t *testing.T) {
	for _, v := range []string{
		"internal/findings/store.go:120", "3519a9a", "msg-9d958ef1a8d0", "/Users/x/y.md", `C:\dev\x.go:12`,
		"file:///etc/hosts", "VAL-bridge", "F-155", "sk-", "sk-short",
		"tasks-queue-handler-with-a-very-long-name-xx", "disk-usage-report-with-a-very-long-name-xx",
		"tokens=5000", "token budget: 4000", "VAL-bridge@cli-agents-bridge", "ESC-bridge@alancurtisagency-payload",
		"ghp_short", "AKIA", "eyJhbGci", "la riga sopra", "passwords are rotated", "my password is Q",
		"passwords=Q",
	} {
		got, hit := Scan(v, nil)
		assert.False(t, hit, "%q was rejected as %s", v, got)
	}
}

// TestScan_SecretAssignmentEveryLength is the CRI B3 grid: any name x any
// operator x a value of length 0..4. A value is one or more characters, so
// lengths 1..4 are rejected and length 0 is not. `name==` is its own case: the
// value there is the second "=".
func TestScan_SecretAssignmentEveryLength(t *testing.T) {
	names := []string{"password", "secret", "token", "passwd", "api_key", "api-key", "apikey", "PASSWORD", "Token"}
	operators := []string{"=", ":", " = ", ": "}
	for _, name := range names {
		for _, op := range operators {
			for n := 0; n <= 4; n++ {
				in := name + op + strings.Repeat("Q", n)
				got, hit := Scan(in, nil)
				if n == 0 {
					assert.False(t, hit, "an empty value must pass: %q -> %s", in, got)
					continue
				}
				require.True(t, hit, "a value of %d character(s) must be rejected: %q", n, in)
				assert.Equal(t, "secret-assignment", got, in)
			}
		}
		got, hit := Scan(name+"==", nil)
		require.True(t, hit, "%q: the value is the second =", name+"==")
		assert.Equal(t, "secret-assignment", got)
	}
}

func TestScan_SecretAssignmentQuotingAndProse(t *testing.T) {
	rejected := []string{
		`password="ab"`, `password='ab'`, `password="a b"`, `"password": "x"`, `{"token":"Q"}`,
		`password = 'Q'`, `Token: 43085`, `max_token=4096`, `secret: no`, `token=ok`,
	}
	for _, v := range rejected {
		got, hit := Scan(v, nil)
		require.True(t, hit, "%q must be rejected", v)
		assert.Equal(t, "secret-assignment", got, v)
	}
	passing := []string{
		`password=""`, `password=''`, `password: `, `password=" "`, `password =`,
		`my password is Q`, `passwords=Q`, `tokens=5000`,
	}
	for _, v := range passing {
		got, hit := Scan(v, nil)
		assert.False(t, hit, "%q was rejected as %s", v, got)
	}
}

func TestScan_URLAndEmailRejected(t *testing.T) {
	for _, v := range []string{
		"https://example.com", "http://host:8080/p?q=1", "ssh://git@host/repo", "HTTPS://EXAMPLE.COM",
		"a@b.co", "first.last+tag@sub.example.org", "git@github.com:org/repo.git",
	} {
		_, hit := Scan(v, nil)
		assert.True(t, hit, "%q must be rejected", v)
	}
}

func TestScan_PointersPass(t *testing.T) {
	for _, v := range []string{
		"internal/findings/store.go:120", "3519a9a", "msg-9d958ef1a8d0", "/Users/x/y.md", `C:\dev\x.go:12`,
		"file:///etc/hosts", "cmd/cab-bridge/main.go:33-84",
	} {
		got, hit := Scan(v, nil)
		assert.False(t, hit, "the pointer %q was rejected as %s", v, got)
	}
}

func TestScan_ConfiguredNamesRejected(t *testing.T) {
	names := []string{"Marta", "Niccolò"}
	rejected := []string{
		"ask Marta about it", "marta", "MARTA said no", "Marta, please", "(Marta)", "Niccolò", "il parere di niccolò",
	}
	for _, v := range rejected {
		got, hit := Scan(v, names)
		require.True(t, hit, "%q must be rejected", v)
		assert.Equal(t, "person-name", got)
	}
	// Whole word only: a name inside a longer word is not that name.
	for _, v := range []string{"Martano", "smarta", "Marta2", "2Marta", "Niccolòzzo"} {
		_, hit := Scan(v, names)
		assert.False(t, hit, "%q must pass: the name is part of a longer word", v)
	}
	// No names configured: the rule does not exist.
	_, hit := Scan("Marta", nil)
	assert.False(t, hit)
}

func TestScan_NamesAreFoundAfterAFirstOccurrenceThatIsPartOfAWord(t *testing.T) {
	got, hit := Scan("Martano and then Marta", []string{"Marta"})
	require.True(t, hit)
	assert.Equal(t, "person-name", got)
}

func TestCheckNames(t *testing.T) {
	assert.NoError(t, CheckNames(nil))
	assert.NoError(t, CheckNames([]string{"Marta", "Al"}))
	for _, bad := range [][]string{{""}, {"A"}, {" Marta"}, {"Marta "}, {"Marta", "x"}} {
		err := CheckNames(bad)
		require.Error(t, err, "%q", bad)
		assert.True(t, strings.HasPrefix(err.Error(), "findings: findings_denied_names["), "every error of this package starts with findings: %q", err.Error())
		for _, entry := range bad {
			if len(strings.TrimSpace(entry)) > 1 {
				assert.NotContains(t, err.Error(), strings.TrimSpace(entry), "the message names the index, never the entry")
			}
		}
	}
}
