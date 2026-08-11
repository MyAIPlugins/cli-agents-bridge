package session

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ScopeSeparator splits a qualified recipient — `VAL-payload@alancurtisagency-payload`
// — into the agent name and the scope that disambiguates it (F-116).
//
// It has to be absent from agent names, or the grammar is not invertible: with a
// name like `VAL@home` there is no way to tell which `@` separates what.
//
// F-124, closed in two lots, and the promise now says exactly what it covers:
//
//	the NAME half     guaranteed HERE (lot 1): one shell word, and a positional
//	                  the verbs accept
//	the SCOPE half    a filesystem path, which nobody controls — so the tool
//	                  RENDERS it (lot 2, internal/shellarg) instead of asking
//	                  the reader to quote it
//
// The reader was asked to quote for a while, and that instruction was not merely
// inconvenient: "always between quotes" holds for a space and breaks on an
// apostrophe, because the reader closes the quote they opened. No rule applied
// by hand covers every path; a renderer does.
//
// Consequence for anyone printing a token: the pastable form is not always the
// same string as the logical one. `peers` quotes its human column and keeps
// `--json` raw; `next` carries `fromAddress` to parse and `fromAddressShellArg`
// to paste.
const ScopeSeparator = "@"

// derivedNameReplacement is what an unusable rune becomes when the name arrives
// from a DIRECTORY NAME rather than from a person. `-` because it is what a name
// is already allowed to contain and it keeps the result readable.
//
// It was introduced for ScopeSeparator alone; F-124 widened the set of runes it
// stands in for, which is why the constant is no longer named after `@`.
const derivedNameReplacement = "-"

// derivedNameFallback is the name for a directory basename with nothing usable
// left in it (`---`, `日本`, or an empty string).
//
// It lives HERE rather than in one caller because three pens derive a name and
// only one of them was guarded: deriveAgentName (naming.go) protected its own
// call site, while derivedAgentName below and Manifest.ApplyV1Defaults — the
// only pen on a READ path — would each have taken the empty string.
const derivedNameFallback = "session"

// markerByte is the escape marker: `_`, chosen because it is already inside the
// grammar and may lead a name.
const markerByte = '_'

// nameSafeRune reports whether a rune may appear in an agent name at all.
//
// The set is deliberately narrow ASCII: letters, digits, `_`, `.`, `-`. Anything
// wider needs a predicate that holds across shells, and "everything except a few
// metacharacters" is the shape that covers the instance in front of you and
// leaves the rule open — which is the mistake this change exists to stop making.
func nameSafeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_', r == '.', r == '-':
		return true
	}
	return false
}

// nameSafeLeadRune is the narrower set allowed at the FRONT: `[A-Za-z0-9_]`.
// `-` because the verbs read it as a flag, `.` because a name that looks like a
// path fragment invites the same confusion for no benefit.
func nameSafeLeadRune(r rune) bool {
	return nameSafeRune(r) && r != '-' && r != '.'
}

// ValidateAgentName rejects a name that cannot be addressed unambiguously.
//
// It refuses the name somebody TYPED. A name DERIVED from a directory takes
// SanitizeDerivedName instead: the two arrive by different routes and deserve
// different answers — one is a mistake to report, the other is a directory the
// caller never chose and must not be punished for (nobody typed anything wrong
// when their worktree happens to be called `feat@2`).
//
// THREE refusals, three different causes, three different sentences. Collapsing
// them into one message would send the reader looking in the wrong place, which
// is what the old "too many arguments — quote the message" did: the message was
// already quoted, and the actual problem was a space in the recipient's name.
//
//	@         the addressing grammar is not invertible (F-116)
//	unlisted  the name is outside the portable grammar (F-124)
//	a lead -  the verbs read it as a flag before any lookup (verbs.go:490)
//
// The middle one says WHAT THE RULE IS and stops there, and getting to that took
// three wrong versions — each one describing a MECHANISM that was false for part
// of the class:
//
//	v1  "the shell splits it before the tool runs, so nothing here could
//	     catch it"        — printed BY the tool, false as you read it
//	v2  "unquoted, a name like this one is split"
//	                      — `a+b` is not split by anything; only whitespace is
//
// The policy is sound and the refusals are right; what kept being wrong was the
// explanation. A rule ("this is the supported grammar") is checkable and stays
// true; a mechanism ("here is what happens to your input") has to hold for every
// member of the class, and this class contains a space, a `+`, a wildcard and a
// multi-byte rune, which the shell treats four different ways.
//
// So: name the grammar, name the risk of pasting something outside it, and claim
// nothing about what became of this particular string.
//
// The empty string is not a name: it is the sentinel asking Register to derive
// one, and `join` validates the flag before knowing whether it was passed.
func ValidateAgentName(name string) error {
	if name == "" {
		return nil
	}
	if strings.Contains(name, ScopeSeparator) {
		return fmt.Errorf("agent name %q cannot contain %q: it is what separates a name from its project when addressing across repositories (e.g. VAL-other%sthe-other-repo)",
			name, ScopeSeparator, ScopeSeparator)
	}
	for _, r := range name {
		if !nameSafeRune(r) {
			return fmt.Errorf("agent name %q cannot contain %q: the supported grammar is letters, digits, %q, %q and %q, so that a name is ONE argument on any shell and can be pasted from `peers` without quoting. A name outside it may still be reachable if you quote it every time, and that is the part nobody does reliably",
				name, string(r), "_", ".", "-")
		}
	}
	if lead := []rune(name)[0]; !nameSafeLeadRune(lead) {
		if lead == '-' {
			return fmt.Errorf("agent name %q cannot start with %q: the loop verbs refuse an argument beginning with a dash as a flag, before any recipient is looked up — and quoting does not help, because the dash is still there once the shell is done",
				name, "-")
		}
		return fmt.Errorf("agent name %q cannot start with %q: the first character has to be a letter, a digit or %q",
			name, string(lead), "_")
	}
	return nil
}

// derivedAgentName is the default agent name for a project path: its basename,
// made addressable. Used by Register when the caller passes no name.
func derivedAgentName(absProj string) string {
	name, _ := SanitizeDerivedName(absProj)
	return name
}

// escapeToken encodes a string into the name grammar so that the original can
// be recovered from the result. It is the injective half of this file.
//
//	a byte outside the grammar  ->  _XX   (uppercase hex of the BYTE)
//	the marker `_` itself       ->  _5F   (FIRST, before anything else)
//	a leading `-` or `.`        ->  _2D / _2E  (only the head is restricted)
//
// THE PROOF IS THE INVERSE, not a table of examples: every `_` in the output is
// followed by exactly two hex digits, so decoding is deterministic — and a
// function with an inverse is injective. That is why the marker has to be
// escaped before anything else: without it a directory literally called `a_2Bb`
// would encode to itself and collide with `a+b`, and the whole property dies on
// that one case.
//
// Bytes rather than runes: a multi-byte rune becomes one escape per byte, which
// keeps the inverse total and needs no knowledge of encodings.
func escapeToken(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == markerByte || c >= 0x80 || !nameSafeRune(rune(c)) {
			fmt.Fprintf(&b, "%c%02X", markerByte, c)
			continue
		}
		b.WriteByte(c)
	}
	out := b.String()
	if out == "" {
		return out
	}
	// The head is escaped the same way as everything else, so the inverse needs
	// no special case for it.
	if lead := rune(out[0]); !nameSafeLeadRune(lead) {
		out = fmt.Sprintf("%c%02X%s", markerByte, out[0], out[1:])
	}
	return out
}

// readableToken is the OTHER repair, and it is deliberately not injective:
// substitute, collapse runs, strip the ends. Used only where a name is being
// cleaned up for a human to read and run, never where one is derived.
func readableToken(s string) string {
	var b strings.Builder
	replacing := false
	for _, r := range s {
		if nameSafeRune(r) {
			b.WriteRune(r)
			replacing = false
			continue
		}
		if !replacing {
			b.WriteString(derivedNameReplacement)
			replacing = true
		}
	}
	return strings.Trim(b.String(), derivedNameReplacement+".")
}

// SanitizeDerivedName makes a directory-derived name addressable, reporting
// whether it had to change so the caller can say so out loud.
//
// The default agent name is the basename of the project path (manager.go), so a
// directory called `foo@bar` or `my repo` would produce an unaddressable name
// with nobody having typed it — and a hard refusal there would fail a `join` for
// a choice the user never made.
//
// It ESCAPES rather than substitutes, and that is the whole of it. Substitution
// is many-to-one by construction: `a+b` and `a:b` both reduce to `a-b` with one
// replacement each — the run-collapse was never the cause — and two directories
// reducing to one name produced two live sessions answering to it, with `tell
// a-b` exiting 1 on "2 live agents are named a-b" (CRI diff-gate P1-2).
//
// A short digest was tried and is NOT enough: a hash with 65536 outputs collides
// whatever you feed it, and the critic built two real paths sharing `e051`.
// Distinct inputs do not give distinct outputs — the codomain is what limits.
// Only a function with an inverse is injective by construction, so escapeToken
// is the derivation and the digest is gone.
//
// AND THE PROPERTY THAT MATTERS IS NOT UNIQUENESS — it is that the name is a
// PURE FUNCTION OF THE DIRECTORY. Uniqueness follows for free from injectivity;
// the reverse does not. Enforcing uniqueness at registration instead (a counter,
// `my-repo-2`) would make the name depend on the ORDER sessions registered in,
// so the same directory could derive a different name after a restart — and a
// derived name that is not stable breaks re-entry, which is the very defect this
// lot is closing. Whoever is tempted to "improve" this with a counter: that is
// what it costs.
//
// A basename already inside the grammar comes back untouched, so the ordinary
// case pays nothing and only the repair is ugly (`my repo` -> `my_20repo`).
//
// WHAT THIS DOES NOT FIX, and it must be said here or it becomes the defect
// nobody looks for: two DIFFERENT projects whose basenames are already identical
// (`alpha/work` and `beta/work`) still derive one name. That collided before
// this change too — Manager.Register has never enforced name uniqueness, its
// BUG-6 check is on the project path — and closing it belongs to a separate lot,
// tracked. Escaping protects names FUSED BY THIS FUNCTION, nothing wider.
func SanitizeDerivedName(absProjectPath string) (string, bool) {
	// THE DOMAIN, stated because the previous version guessed at it: filepath.Base
	// NEVER returns an empty string — `Base("")` is `"."`, `Base("/")` is `"/"` —
	// and escapeToken produces at least one byte for every input byte. So the
	// result cannot be empty, and the fallback branch that used to sit here was
	// dead code described as a fragile precondition (CRI re-gate). Removed rather
	// than kept: an unreachable branch with a comment explaining when it fires is
	// how a reader learns something untrue about the function.
	//
	// derivedNameFallback stays for SuggestAddressableName, which repairs an
	// arbitrary NAME rather than a path and can genuinely be left with nothing.
	base := filepath.Base(absProjectPath)
	out := escapeToken(base)
	return out, out != base
}

// SuggestAddressableName turns an EXISTING agent name into a valid one, for the
// repair `join` offers when it meets a session named before the grammar.
//
// It uses readableToken, NOT the escape, and the difference is deliberate: this
// is a suggestion somebody reads and runs, so `ESC bridge` should be offered as
// `ESC-bridge` and not `ESC_20bridge`. Injectivity is not needed here because
// nothing is being derived — if the suggested name is already taken by a live
// session, join's existing guard refuses and names the clash, which is the right
// answer and one nobody has to build here.
func SuggestAddressableName(name string) string {
	if out := readableToken(name); out != "" {
		return out
	}
	return derivedNameFallback
}
