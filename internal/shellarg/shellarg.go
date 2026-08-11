// Package shellarg renders a string as a single POSIX shell argument.
//
// It exists because of F-124: the bridge prints tokens and whole commands that
// agents and humans copy into a shell, and a value the bridge does not control —
// a filesystem path, a name written before the grammar, raw user input — turns
// one argument into several, or into something the shell executes.
//
// WHY A RENDERER AND NOT AN INSTRUCTION. The skills used to say "always between
// quotes", and that rule is not merely inconvenient, it is WRONG: it holds for a
// space and breaks on an apostrophe, because the reader closes the quote they
// opened. Measured:
//
//	tell 'VAL-x@/Users/alan/Alan's Project'   ->   zsh:1: unmatched '
//
// No rule a person applies by hand covers every case. Moving the algorithm from
// the reader to the renderer is the whole point.
package shellarg

import "strings"

// singleQuote is the POSIX-portable form: inside single quotes every byte is
// literal, including backslashes and dollars — the ONLY character needing
// treatment is the single quote itself, which is closed, escaped and reopened.
const singleQuote = "'"

// safe reports whether a byte can be left bare, with no quoting at all.
//
// Deliberately narrow, and NOT "everything except metacharacters": that shape
// covers the instances you thought of and leaves the rule open, which is the
// mistake this package exists to stop making. If a byte is not on this list it
// gets quoted, and quoting something that did not need it costs two characters.
func safe(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_', c == '-', c == '.', c == '/', c == '@', c == ':', c == ',', c == '+', c == '=':
		return true
	}
	return false
}

// Quote returns s as ONE shell word for POSIX sh.
//
// THE CONTRACT: evaluating the result yields exactly s as a single argv entry,
// with no globbing, no substitution and no side effects. A string already made
// of safe bytes comes back untouched, so the ordinary case reads exactly as it
// does today.
//
// OUT OF CONTRACT, stated rather than discovered: a tab or a newline is quoted
// correctly and survives the shell, but it destroys any line-oriented surface it
// is printed on — a table row, an error message, a command an agent reads. The
// value round-trips; the DISPLAY does not, and callers rendering to a line must
// not assume otherwise.
func Quote(s string) string {
	if s == "" {
		return singleQuote + singleQuote
	}
	bare := true
	for i := 0; i < len(s); i++ {
		if !safe(s[i]) {
			bare = false
			break
		}
	}
	if bare {
		return s
	}
	return singleQuote + strings.ReplaceAll(s, singleQuote, `'\''`) + singleQuote
}
