package main

import (
	"fmt"
	"os"
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

// effectiveScope answers the question the whole feature actually asks — WHICH
// PROJECT DOES THIS SESSION BELONG TO — for a manifest that may predate the
// field (F-17), and says whether it could answer at all.
//
// This exists because `""` meant two different things and nobody had ever
// decided which: "this session has no project" and "I do not know this
// session's project". Every reader picked one, and we corrected THREE
// COMPARISONS — next.go, verbs.go, send.go — without ever fixing the VALUE. The
// fourth face was not a comparison at all: `collectPeers` treats an empty scope
// filter as NO FILTER, so a legacy session searched the entire data dir,
// resolved a bare name in another repository, and — since an unknown scope is
// not a crossing — delivered esc→val across projects with nobody having typed a
// project. Silent misrouting, not a bypassed restriction (CRI final gate).
//
// A legacy session's project is derivable: it is the same git-common-root that
// registration and the F-27 backfill already compute from ProjectPath. Derived,
// it behaves like any current session — no global search, no exemption — and
// F-6 stays closed because within its own repository the derived scope is equal.
// The two opposite defects disappear together, which is the sign the root was
// one.
//
// UNKNOWN is a value, not a wildcard: a session whose project cannot be derived
// matches only other sessions in the same condition. Treating it as "matches
// everything" is precisely what produced the defect above.
func effectiveScope(mf *session.Manifest) (string, bool) {
	if mf == nil {
		return "", false
	}
	if mf.Scope != "" {
		return mf.Scope, true
	}
	if mf.ProjectPath == "" {
		return "", false
	}
	// Silent on failure, unlike resolveScope: that one runs on the caller's own
	// cwd where a warning is actionable, while this runs over other people's
	// manifests where an unresolvable path is just an old session.
	home, _ := os.UserHomeDir()
	root, err := session.FindProjectRoot(mf.ProjectPath, home)
	if err != nil || root == "" {
		return "", false
	}
	if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
		return resolved, true
	}
	return root, true
}

// effectiveScopeCache derives a session's project once per session id: the
// derivation walks the filesystem, and a peer list is read on every send.
func effectiveScopeCache(mgr *session.Manager) func(sessionID string) (string, bool) {
	type entry struct {
		scope string
		known bool
	}
	seen := map[string]entry{}
	return func(sessionID string) (string, bool) {
		if e, ok := seen[sessionID]; ok {
			return e.scope, e.known
		}
		var e entry
		if mf, err := mgr.LoadManifest(sessionID); err == nil {
			e.scope, e.known = effectiveScope(mf)
		}
		seen[sessionID] = e
		return e.scope, e.known
	}
}

// sameProject compares two sessions by their EFFECTIVE scope, unknown included:
// two sessions that cannot say where they are belong together and to nobody
// else.
func sameProject(aScope string, aKnown bool, bScope string, bKnown bool) bool {
	if aKnown != bKnown {
		return false
	}
	if !aKnown {
		return true // both unknown: one group, and it reaches no real repository
	}
	return aScope == bScope
}

// crossesScopes reports whether a message between these two scopes leaves its
// project — the question the val→val restriction is asked.
//
// An UNKNOWN scope is not a different scope. A legacy session (pre-F-17) has an
// empty one, and comparing strings made `"" != "/repo/x"` read as a crossing:
// on a binary carrying this feature, such a session could no longer send a
// PLAIN, in-scope `ask` — refused with "across projects only a val writes to a
// val", about a project it never named (CRI2 F-6). Reproduced.
//
// It is the same conflation `newNextMessage` had, in another file of the same
// commit — and the comment there names it: not knowing and being different are
// different things. Having written that sentence and not grepped for the
// pattern it describes is how this one survived; the shared function is so that
// there is nothing left to grep.
//
// In doubt the restriction does NOT apply: it is a restriction, and refusing on
// a guess costs a message that was legitimate.
func crossesScopes(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a != b
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
