package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The addressing grammar of F-116, format and parse kept adjacent because the
// only property that matters spans both: EVERY TOKEN `peers` PRINTS CAN BE
// PASTED INTO A COMMAND.
//
// Break that and the discovery list goes back to being wider than reachability
// — an agent reads a name it cannot address — which is the defect F-116 exists
// to close, reappearing on the branch meant to fix it.
//
// Two forms, and the second is not decoration: `peers` falls back to the full
// path when two scopes share a basename (scopeColumn), so the qualified form has
// to accept a path too, or the ambiguous rows become unaddressable.
//
//	VAL-payload                              in my own scope, exactly as before
//	VAL-payload@alancurtisagency-payload     the repository's basename
//	VAL-payload@/Users/alan/develop/thing    the full path, when a basename is ambiguous
//
// The name can never contain the separator (session.ValidateAgentName), so the
// FIRST separator splits, and everything after it is the scope — including a
// path that contains one.

// recipient is a parsed destination. Scope empty means "my own", i.e. exactly
// the pre-F-116 behaviour.
type recipient struct {
	name  string
	scope string // basename or absolute path; empty = caller's own scope
}

func (r recipient) qualified() bool { return r.scope != "" }

// String is the format half. The round-trip test pins format→parse→format.
func (r recipient) String() string {
	if !r.qualified() {
		return r.name
	}
	return r.name + session.ScopeSeparator + r.scope
}

// parseRecipient splits a destination token. It never guesses: an empty half is
// an error naming both forms, because a bare `@repo` or `VAL@` is a typo whose
// silent interpretation would send a message somewhere nobody asked.
func parseRecipient(token string) (recipient, error) {
	name, scope, found := strings.Cut(token, session.ScopeSeparator)
	if !found {
		return recipient{name: token}, nil
	}
	if name == "" || scope == "" {
		return recipient{}, fmt.Errorf("%q is not a destination: write `<agent>` for this project, or `<agent>%s<project>` for another one (the project is the SCOPE column of `peers --all-scopes`)",
			token, session.ScopeSeparator)
	}
	return recipient{name: name, scope: scope}, nil
}

// scopeMatchesHint reports whether a session's scope is the one a qualified
// address names.
//
// An absolute hint is compared whole; anything else is compared against the
// basename — which is what `peers` prints, and the reason the two forms exist.
//
// "Absolute" is filepath.IsAbs, not a leading "/" (F-135): on Windows an
// absolute path starts with a drive letter, so the old test sent EVERY
// qualified address down the basename branch, where a full path can never
// match. The error it produced named the project it had just refused to find.
//
// And IsAbs alone is not enough. The stored scope carries the casing the
// resolver read off the disk; the hint carries the casing a human typed. So
// the hint goes through the SAME canonicalisation as the scope, and the
// comparison is the one this OS uses for file names.
func scopeMatchesHint(sessionScope, hint string) bool {
	if sessionScope == "" {
		return false // legacy session with no scope: never matched by an address
	}
	if filepath.IsAbs(hint) {
		return session.SamePathLexical(session.CanonicalizePath(hint), sessionScope)
	}
	return session.SamePathComponent(filepath.Base(sessionScope), hint)
}

// volumeHint explains a failure whose cause the plain message cannot show: a
// hint that is ROOTED BUT NAMES NO VOLUME — `/foo` typed literally on Windows.
//
// It resolves against whichever drive the process happens to be on, so it is not
// a full path; and it is not a basename either, because it carries a separator.
// Without this line the reader gets "no agent named X in project /foo" and has
// no way to see that the shape of the address is the problem.
//
// It ADDS to the message, never replaces it: the project list is still the
// answer to "wrong name or wrong project?".
//
// Deliberately here and NOT in parseRecipient, where the CRI design put it.
// parseRecipient is pure syntax and is used for two things: what a human types,
// and the round-trip of what the product itself printed. Refusing there would
// have meant refusing a token `peers` had just offered — the same defect as
// F-135, upside down — and four tests defend that invariant by name
// (TestRecipient_RoundTrip, TestNextMessage_CrossScopeCarriesTheLogicalAddress,
// TestResolveRecipient_AmbiguousBasenameFailsClosed,
// TestSoleSessionNamed_AmbiguousBasenamesGetDistinctWorkingTokens).
//
// On Unix session.PathNeedsVolume is always false, so this returns "" and no
// message changes: a leading "/" IS the root there, and nothing is ambiguous.
func volumeHint(scope string) string {
	if !session.PathNeedsVolume(scope) {
		return ""
	}
	return fmt.Sprintf(" — note: %q contains a path separator but names no drive, so on this host it is not a full path: "+
		"use the form `peers --all-scopes` prints (a drive path or a UNC path), or just the project folder name", scope)
}
