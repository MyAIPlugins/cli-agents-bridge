package session

import (
	"fmt"
	"hash/fnv"
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
//	unlisted  the name is outside the portable grammar (F-124)
//	a lead -  the verbs read it as a flag before any lookup (verbs.go:490)
//
// The middle one used to explain itself by saying the shell splits the name
// "before the tool ever runs — so nothing here could catch it", which was FALSE
// in a way that reading it out loud exposes: the tool is what prints it. Quoted,
// `ESC bridge` and `a+b` arrive whole and are refused by POLICY; unquoted, a
// space splits them and the tool receives the pieces. Two different facts, and
// the error can only honestly claim the first — the second is why the policy
// exists, not what happened to this input.
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
			return fmt.Errorf("agent name %q cannot contain %q: an agent name has to be ONE argument on any shell and on any platform, so the supported grammar is letters, digits, %q, %q and %q. Unquoted, a name like this one is split before the tool sees it; quoted it arrives whole and is still refused, because a recipient that only works when it is quoted is a recipient that will be pasted wrong",
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

// sanitizeToken is the repair itself, with no policy attached: substitute,
// collapse, strip. It returns "" when nothing usable is left, and the two
// callers below decide what that means for them.
//
// Three rules, written down because not one can be read off the code:
//
//	every rune outside nameSafeRune becomes `-`
//	a RUN of them collapses to a single `-`   (`a   b` -> `a-b`, not `a---b`)
//	leading and trailing `-` and `.` are stripped, so the result can lead
func sanitizeToken(s string) string {
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
// It takes the ABSOLUTE PATH, not the basename, and that is the whole of the
// injectivity fix. Substitution is many-to-one by construction: `a+b` and `a:b`
// both reduce to `a-b`, with a single replacement each — so the collapse is an
// aggravator, not the cause, and dropping it would fix nothing. Two directories
// that reduce to one name got two live sessions answering to it and `tell a-b`
// exiting 1 with "2 live agents are named a-b" (CRI diff-gate P1-2, reproduced).
//
// So when a substitution actually happened, the name carries a short digest OF
// THE PATH. Distinct directories have distinct paths by definition, which makes
// the result injective in the directory rather than merely unlikely to clash —
// the residual case needs a real 16-bit digest collision between two paths, and
// that one fails closed with the message above.
//
// A basename that is already addressable is returned untouched, no digest: the
// ordinary case stays readable, and the cost lands only where the repair does.
//
// WHAT THIS DOES NOT FIX, and it must be said here or it becomes the defect
// nobody looks for: two DIFFERENT projects whose basenames are already identical
// (`alpha/work` and `beta/work`) still derive one name. That collided before
// this change too — Manager.Register has never enforced name uniqueness, its
// BUG-6 check is on the project path — and closing it belongs to a separate lot,
// tracked. The digest protects names FUSED BY THIS FUNCTION, nothing wider.
func SanitizeDerivedName(absProjectPath string) (string, bool) {
	base := filepath.Base(absProjectPath)
	out := sanitizeToken(base)
	if out == "" {
		out = derivedNameFallback
	}
	if out == base {
		return out, false
	}
	return out + derivedNameReplacement + shortDigest(absProjectPath), true
}

// SuggestAddressableName turns an EXISTING agent name into a valid one, for the
// repair `join` offers when it meets a session named before the grammar.
//
// Deliberately no digest: this is not a derivation and needs no injectivity —
// it is a readable suggestion for a human or an agent to run, and `ESC bridge`
// should become `ESC-bridge`, not `ESC-bridge-4f21`. If that name is taken by a
// live session, join's existing guard refuses and names the clash, which is the
// right answer and one nobody has to build here.
func SuggestAddressableName(name string) string {
	if out := sanitizeToken(name); out != "" {
		return out
	}
	return derivedNameFallback
}

// shortDigest is 16 bits of FNV-1a as four hex digits: stable across runs and
// across versions (the algorithm is fixed), so the same directory always derives
// the same name and an agent can re-read it from `join` instead of remembering
// it — which is the property that matters for LL-13, since the danger is in
// identifiers that get RETYPED, not in ones that get read.
func shortDigest(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%04x", h.Sum32()&0xffff)
}
