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
type contentRule struct {
	name string
	re   *regexp.Regexp
}

// personNameRule is the 13th rule: it is a list from config, not a pattern.
const personNameRule = "person-name"

// contentRules are the 12 pattern rules, in the order they are applied: the
// first that matches names the refusal, so a key shaped like both Anthropic and
// OpenAI keys is reported as Anthropic.
//
// Known and wanted false positives: `Token: 43085`, `max_token=4096`,
// `secret: no`, `token=ok` (rule 10) and `git@github.com:org/repo.git`
// (rule 12). A refused row costs one rewrite; a leaked secret does not come back.
var contentRules = []contentRule{
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AIPA|ANPA|ANVA|AROA)[A-Z0-9]{16}\b`)},
	{"aws-secret-key", regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)},
	{"github-fine-grained-token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,255}`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`)},
	{"slack-token", regexp.MustCompile(`\b(?:xox[abprs]|xapp)-[A-Za-z0-9-]{10,}`)},
	{"pem-private-key", regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)},
	// A value is one or more characters after the name, the operator and an
	// optional opening quote: there is no minimum length, because the BAR asks
	// to refuse the assignment, not only the long ones. An empty value passes.
	{"secret-assignment", regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key)["']?\s*[:=]\s*["']?[^\s"',;]+`)},
	{"url", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/?#]+`)},
	{"email", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)},
}

// minNameRunes is the shortest accepted entry of findings_denied_names.
const minNameRunes = 2

// Scan returns the name of the first rule the value breaks. names is the list
// of person names from the configuration (may be empty: then rule 13 does not
// exist, and the caller says so).
func Scan(value string, names []string) (rule string, hit bool) {
	for _, r := range contentRules {
		if r.re.MatchString(value) {
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
