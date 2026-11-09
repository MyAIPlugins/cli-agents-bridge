package findings

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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
	confirm func(candidate string) bool
}

func (r contentRule) matches(value string) bool {
	if r.confirm == nil {
		return r.re.MatchString(value)
	}
	for _, candidate := range r.re.FindAllString(value, -1) {
		if r.confirm(candidate) {
			return true
		}
	}
	return false
}

// personNameRule is the 13th rule: it is a list from config, not a pattern.
const personNameRule = "person-name"

// notAfterAlnum is the left context of the generic `sk-` prefix: the start of
// the value or anything but a letter or a digit. It is NOT `\b`: `_` is a word
// character, so `\b` let `prefix_sk-...` through. It is not "nothing" either:
// `chiave` is a kebab-case slug, and `task-description-overflow` contains
// `sk-description-overflow`, which the 20-character body would take for a key.
// A key glued directly to a letter or a digit is therefore a declared limit.
const notAfterAlnum = `(?:^|[^A-Za-z0-9])`

// contentRules are the 12 pattern rules, in the order they are applied: the
// first that matches names the refusal, so a key shaped like both Anthropic and
// OpenAI keys is reported as Anthropic.
//
// The rules whose prefix is unmistakable (AKIA, github_pat_, ghp_, xoxb-, a
// scheme before ://) have no boundary at all, on either side: a boundary is
// where a secret escapes, by being glued to a word character (`token_ghp_...`,
// `AKIA...EXAMPLE_PROD`). That costs the false positives below.
//
// Known and wanted false positives: `Token: 43085`, `max_token=4096`,
// `secret: no`, `token=ok` (rule 10), `git@github.com:org/repo.git` (rule 12) and
// an all-caps word of 20 characters that starts like an AWS key id
// (`ASIAPACIFICREGIONCODE`). A refused row costs one rewrite; a leaked secret does
// not come back.
//
// Declared limits of the left context: a `sk-` key (rules 5 and 6) or a JWT glued
// directly to a letter or a digit is not recognised, and neither is a secret of a
// type that is not listed here, split across the value, or encoded.
var contentRules = []contentRule{
	{name: "aws-access-key", re: regexp.MustCompile(`(?:AKIA|ASIA|AGPA|AIDA|AIPA|ANPA|ANVA|AROA)[A-Z0-9]{16}`)},
	{name: "aws-secret-key", re: regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)},
	{name: "github-fine-grained-token", re: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,255}`)},
	{name: "github-token", re: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,255}`)},
	{name: "anthropic-key", re: regexp.MustCompile(notAfterAlnum + `sk-ant-[A-Za-z0-9_-]{20,}`)},
	{name: "openai-key", re: regexp.MustCompile(notAfterAlnum + `sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`)},
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
// of person names from the configuration (may be empty: then rule 13 does not
// exist, and the caller says so).
func Scan(value string, names []string) (rule string, hit bool) {
	for _, r := range contentRules {
		if r.matches(value) {
			return r.name, true
		}
	}
	for _, name := range names {
		if containsWholeWord(value, name) {
			return personNameRule, true
		}
	}
	return "", false
}

// CheckNames validates findings_denied_names: an empty entry, one shorter than
// two characters or one with spaces at the edges is a configuration error. The
// message carries the index, never the entry (it may be a person's name).
func CheckNames(names []string) error {
	for i, name := range names {
		if utf8.RuneCountInString(name) < minNameRunes || name != strings.TrimSpace(name) {
			return fmt.Errorf("findings_denied_names[%d] is invalid: it must have at least %d characters and no space at the start or the end", i, minNameRunes)
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
