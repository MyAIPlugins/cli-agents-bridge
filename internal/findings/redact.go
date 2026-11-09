package findings

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// verdict is what a rule says about a value. A rule that decides by structure can
// also say that it could not decide: the work it was allowed ran out, and a value
// that cannot be ruled out is refused, but not for the reason a hit is.
type verdict uint8

const (
	verdictNo verdict = iota
	verdictHit
	verdictUnchecked
)

// contentRule is one pattern a column value must not match. The name is what
// the refusal reports; it never carries a prefix of the secret.
//
// re finds the candidates. When confirm is set it decides whether a candidate
// really is the secret (the JWT rule: a shape is not enough, the structure
// decides); when it is nil every match of re is a hit.
type contentRule struct {
	name    string
	re      *regexp.Regexp
	confirm func(candidate string) verdict
}

// matches returns verdictHit if some candidate is the secret, whatever the others
// are; verdictUnchecked if none is but one could not be examined; verdictNo
// otherwise.
func (r contentRule) matches(value string) verdict {
	if r.confirm == nil {
		if r.re.MatchString(value) {
			return verdictHit
		}
		return verdictNo
	}
	result := verdictNo
	for _, candidate := range r.re.FindAllString(value, -1) {
		switch r.confirm(candidate) {
		case verdictHit:
			return verdictHit
		case verdictUnchecked:
			result = verdictUnchecked
		}
	}
	return result
}

// personNameRule is the 14th rule: it is a list from config, not a pattern.
const personNameRule = "person-name"

// prefixedKeys are the prefixes that say what a key is: after one of them, 20 more
// characters of the key alphabet are a key, wherever the prefix sits (no boundary
// on the left). The rules below and the help are both built from this list, so the
// help names exactly the prefixes that are refused.
var prefixedKeys = []struct{ rule, prefix string }{
	{"anthropic-key", "sk-ant-"},
	{"openai-key", "sk-proj-"},
	{"openai-key", "sk-svcacct-"},
	{"openai-key", "sk-admin-"},
}

// PrefixedKeyPrefixes lists the prefixes of prefixedKeys, in order. A name that
// contains one of them followed by 20 characters is refused even if it is not a
// key; the help says so.
func PrefixedKeyPrefixes() []string {
	out := make([]string, len(prefixedKeys))
	for i, k := range prefixedKeys {
		out[i] = k.prefix
	}
	return out
}

// prefixedKeyPattern is the alternation of the prefixes of one rule, then 20 or
// more characters of the key alphabet.
func prefixedKeyPattern(rule string) string {
	var prefixes []string
	for _, k := range prefixedKeys {
		if k.rule == rule {
			prefixes = append(prefixes, regexp.QuoteMeta(k.prefix))
		}
	}
	return `(?:` + strings.Join(prefixes, "|") + `)[A-Za-z0-9_-]{20,}`
}

// openAIMarkerPattern is `sk-`, anything in the key alphabet, the marker, and at
// least one more character of the alphabet. It is the third way to recognise an
// OpenAI key, next to the three prefixed forms and the exact legacy shape: it does
// not depend on the prefix or on the length, so it also takes a legacy key of any
// length, `sk-None-` and `sk-service-<name>-`. The marker `T3BlbkFJ` (base64 of
// "OpenAI") is what gitleaks (`cmd/generate/config/rules/openai.go`, keyword
// "T3BlbkFJ") and trufflehog (`pkg/detectors/openai/openai.go`, "The magic string
// T3BlbkFJ") key on; both files read on 2026-11-09. OpenAI does not document the
// format in any page consulted, so this is an observed fact and not a guarantee,
// which is why the other two forms stay. It cannot fire on a kebab-case slug: the
// marker is not a word.
const openAIMarkerPattern = `sk-[A-Za-z0-9_-]*T3BlbkFJ[A-Za-z0-9_-]+`

// contentRules are the 13 pattern rules, in the order they are applied: the
// first that matches names the refusal, so a key shaped like both Anthropic and
// OpenAI keys is reported as Anthropic.
//
// The rules whose prefix is unmistakable (AKIA, github_pat_, ghp_, xoxb-, sk-ant-,
// sk-proj-, a scheme before ://) have no boundary at all, on either side: a
// boundary is where a secret escapes, by being glued to a word character
// (`token_ghp_...`, `AKIA...EXAMPLE_PROD`). The generic `sk-` prefix is not
// unmistakable (`task-description-overflow` contains `sk-description-overflow`),
// so the old OpenAI form is only taken in its exact shape: 48 letters and digits,
// no dash (openai-key). The jwt rule tries every place in a
// segment as the start of the header, so it has no left boundary either.
//
// Known and wanted false positives: `Token: 43085`, `max_token=4096`,
// `secret: no`, `token=ok` (secret-assignment), `git@github.com:org/repo.git` (email), an
// all-caps word of 20 characters that starts like an AWS key id
// (`ASIAPACIFICREGIONCODE`), a kebab-case slug that contains one of the prefixes
// of prefixedKeys followed by 20 more characters
// (`task-admin-console-timeout-fix-on-windows`), and a pointer in which `sk-`, the
// end of `disk-` or `task-`, is followed by a digest of 48 letters and digits
// (`/tmp/disk-<sha256>.log`, `cache/task-<64 hex digits>.bin`): the legacy OpenAI
// form has no boundary on the left, and putting one back would lose keys glued to
// a word. A refused row costs one rewrite (cite the file with another pointer: a
// commit and a line); a leaked secret does not come back.
//
// Declared limits: a secret of a type that is not listed here, split across the
// value, or encoded; a JWT in the JSON serialization; a key of another vendor that
// starts with `sk-` (the generic prefix used to take it by accident, and no longer
// does unless it has the legacy shape or the OpenAI marker).
var contentRules = []contentRule{
	{name: "aws-access-key", re: regexp.MustCompile(`(?:AKIA|ASIA|AGPA|AIDA|AIPA|ANPA|ANVA|AROA)[A-Z0-9]{16}`)},
	{name: "aws-secret-key", re: regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)},
	{name: "github-fine-grained-token", re: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,255}`)},
	{name: "github-token", re: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,255}`)},
	{name: "anthropic-key", re: regexp.MustCompile(prefixedKeyPattern("anthropic-key"))},
	{name: "openai-key", re: regexp.MustCompile(prefixedKeyPattern("openai-key") + `|sk-[A-Za-z0-9]{48}`)},
	{name: "openai-key-marker", re: regexp.MustCompile(openAIMarkerPattern)},
	{name: "slack-token", re: regexp.MustCompile(`(?:xox[abprs]|xapp)-[A-Za-z0-9-]{10,}`)},
	{name: "pem-private-key", re: regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	{name: "jwt", re: jwtCandidate, confirm: containsJWT},
	// A value is one or more characters after the name, the operator and an
	// optional opening quote: there is no minimum length, because the BAR asks
	// to refuse the assignment, not only the long ones. An empty value passes.
	{name: "secret-assignment", re: regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key)["']?\s*[:=]\s*["']?[^\s"',;]+`)},
	{name: "url", re: regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s/?#]+`)},
	{name: "email", re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)},
}

// ContentRuleNames lists the rules a value is checked against, in the order they
// are applied, the person-name rule last. The help prints them.
func ContentRuleNames() []string {
	names := make([]string, 0, len(contentRules)+1)
	for _, r := range contentRules {
		names = append(names, r.name)
	}
	return append(names, personNameRule)
}

// minNameRunes is the shortest accepted entry of findings_denied_names.
const minNameRunes = 2

// Scan returns the name of the first rule the value breaks. names is the list
// of person names from the configuration (may be empty: then the person-name
// rule does not exist, and the caller says so).
func Scan(value string, names []string) (rule string, hit bool) {
	rule, v := scan(value, names)
	return rule, v != verdictNo
}

// scan is Scan with the verdict: the command needs to tell a value that is a
// secret from one that could not be examined.
func scan(value string, names []string) (rule string, v verdict) {
	for _, r := range contentRules {
		if v := r.matches(value); v != verdictNo {
			return r.name, v
		}
	}
	for _, name := range names {
		if containsWholeWord(value, name) {
			return personNameRule, verdictHit
		}
	}
	return "", verdictNo
}

// CheckNames validates findings_denied_names: an empty entry, one shorter than
// two characters or one with spaces at the edges is a configuration error. The
// message carries the index, never the entry (it may be a person's name).
func CheckNames(names []string) error {
	for i, name := range names {
		if utf8.RuneCountInString(name) < minNameRunes || name != strings.TrimSpace(name) {
			return fmt.Errorf("findings: findings_denied_names[%d] is invalid: it must have at least %d characters and no space at the start or the end", i, minNameRunes)
		}
	}
	return nil
}

// containsWholeWord reports whether name occurs in value as a whole word,
// ignoring case: the characters before and after the occurrence are not letters
// or digits. \b is not used because Go defines it on ASCII only, and a name like
// "Niccolò" would break. Comparison is per rune, so lowercasing cannot shift offsets.
func containsWholeWord(value, name string) bool {
	hay := lowerRunes(value)
	needle := lowerRunes(name)
	if len(needle) == 0 {
		return false
	}
	for start := 0; start+len(needle) <= len(hay); start++ {
		if !runesEqual(hay[start:start+len(needle)], needle) {
			continue
		}
		before := start == 0 || !isWordRune(hay[start-1])
		end := start + len(needle)
		after := end == len(hay) || !isWordRune(hay[end])
		if before && after {
			return true
		}
	}
	return false
}

func lowerRunes(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		out = append(out, unicode.ToLower(r))
	}
	return out
}

func runesEqual(a, b []rune) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
