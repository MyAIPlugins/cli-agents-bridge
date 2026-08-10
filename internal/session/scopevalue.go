package session

import (
	"os"
	"path/filepath"
)

// The project a session belongs to, and everything that decides on it.
//
// It lives HERE, in the package where the decisions are, and not in cmd where
// it started: three completion criteria in a row drew a boundary — the value,
// the field, the cmd package — and three times the defect landed just outside
// it. The last one was `LookupByCWDDetails`, which groups sessions by scope to
// raise the anti-impersonation warning and could not even reach a helper that
// lived in cmd.
//
// The criterion that has no perimeter names the DECISION instead of a place:
// every point that decides "which project is this session in" gets the answer
// from the same function.

// EffectiveScope answers the question the whole feature actually asks — WHICH
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
func EffectiveScope(mf *Manifest) string {
	if mf == nil {
		return ""
	}
	if mf.Scope != "" {
		return mf.Scope
	}
	if mf.ProjectPath == "" {
		return ""
	}
	// Silent on failure, unlike resolveScope: that one runs on the caller's own
	// cwd where a warning is actionable, while this runs over other people's
	// manifests where an unresolvable path is just an old session.
	home, _ := os.UserHomeDir()
	root, err := FindProjectRoot(mf.ProjectPath, home)
	if err != nil || root == "" {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
		return resolved
	}
	return root
}

// ScopeIsDerived reports whether this session's project had to be worked out
// rather than read — the thing a human is entitled to be told, since a derived
// scope is a fact about the filesystem now and not about the session's record.
func ScopeIsDerived(mf *Manifest) bool {
	return mf != nil && mf.Scope == "" && EffectiveScope(mf) != ""
}

// EffectiveScopeCache derives a session's project once per session id: the
// derivation walks the filesystem, and a peer list is read on every send.
func (m *Manager) EffectiveScopeCache() func(sessionID string) string {
	seen := map[string]string{}
	return func(sessionID string) string {
		if s, ok := seen[sessionID]; ok {
			return s
		}
		s := ""
		if mf, err := m.LoadManifest(sessionID); err == nil {
			s = EffectiveScope(mf)
		}
		seen[sessionID] = s
		return s
	}
}

// SameProject compares two EFFECTIVE scopes, with "" meaning UNKNOWN.
//
// It is a plain equality, and that is the point: once the value carries its own
// meaning, unknown==unknown is one group — sessions that cannot say where they
// are reach each other and no real repository — and every other case falls out.
// The comparisons that needed special-casing needed it because the VALUE was
// ambiguous, not because comparing was hard.
func SameProject(a, b string) bool { return a == b }

// CrossesScopes reports whether a message between these two scopes leaves its
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
func CrossesScopes(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a != b
}
