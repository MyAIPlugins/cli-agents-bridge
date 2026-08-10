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
func scopeMatchesHint(sessionScope, hint string) bool {
	if sessionScope == "" {
		return false // legacy session with no scope: never matched by an address
	}
	if strings.HasPrefix(hint, "/") {
		return filepath.Clean(sessionScope) == filepath.Clean(hint)
	}
	return filepath.Base(sessionScope) == hint
}
