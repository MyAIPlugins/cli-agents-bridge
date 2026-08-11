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
// F-124 NOTE, and it is the reason the sentence above is shorter than it was:
// this used to end by claiming the rule the whole addressing rests on is that
// every token `peers` prints can be pasted into a command. That rule is NOT true
// today, and saying it here made it look settled. What IS true after this lot:
// an agent NAME is guaranteed to survive a shell and to be read as a recipient.
// The SCOPE half of a qualified address is not — a project path may contain a
// space, `peers` prints it raw, and `next` puts the full path in `fromAddress`
// on every cross-project message. Until that is fixed a qualified address has to
// be quoted by whoever pastes it.
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
//	a space   the shell does not deliver the name as one argument (F-124)
//	a lead -  the verbs read it as a flag before any lookup (verbs.go:490)
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
			return fmt.Errorf("agent name %q cannot contain %q: a recipient has to reach the tool as ONE argument, and a shell splits or rewrites this one before the tool ever runs — so nothing here could catch it. Letters, digits, %q, %q and %q only",
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
	name, _ := SanitizeDerivedName(filepath.Base(absProj))
	return name
}

// SanitizeDerivedName makes a directory-derived name addressable, reporting
// whether it had to change so the caller can say so out loud.
//
// The default agent name is filepath.Base of the project path (manager.go), so a
// directory called `foo@bar` or `my repo` would produce an unaddressable name
// with nobody having typed it — and a hard refusal there would fail a `join` for
// a choice the user never made.
//
// Four rules, written down because not one of them can be read off the code:
//
//	every rune outside nameSafeRune becomes `-`
//	a RUN of them collapses to a single `-`   (`a   b` -> `a-b`, not `a---b`)
//	leading and trailing `-` and `.` are stripped, so the result can lead
//	nothing usable left -> derivedNameFallback, never the empty string
//
// The collapse costs injectivity — `my repo` and `my  repo` land on one name —
// and that is affordable here: two sessions in one directory are already refused
// (F-90), and join's cross-scope guard catches a name already taken elsewhere.
func SanitizeDerivedName(base string) (string, bool) {
	var b strings.Builder
	replacing := false
	for _, r := range base {
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
	out := strings.Trim(b.String(), derivedNameReplacement+".")
	if out == "" {
		out = derivedNameFallback
	}
	return out, out != base
}
