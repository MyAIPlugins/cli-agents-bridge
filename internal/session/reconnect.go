package session

import (
	"errors"
	"fmt"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// errReuseNoMatch is the internal sentinel meaning "no session matched the
// identity" — Register treats it as "fall through to a fresh register" (the
// idempotent reconnect-or-register behaviour). Never surfaced to callers.
var errReuseNoMatch = errors.New("reuse: no matching session")

// ErrAmbiguousResume and ErrUnaddressableResume are EXPORTED because they are
// the two states a resume refuses to guess its way out of, and a caller has to
// be able to tell them from a plain failure — they mean "say which one" and
// "repair it first", which are different answers.
//
// Both are deliberately NOT errReuseNoMatch: falling through to a fresh
// registration is exactly the behaviour that abandoned a mailbox.
var (
	ErrAmbiguousResume     = errors.New("reuse: several identities here")
	ErrUnaddressableResume = errors.New("reuse: the session here has a name that cannot be addressed")
)

// distinctNames lists the agent names among candidates, once each, in a stable
// order so the error message does not shuffle between runs.
func distinctNames(matches []identityMatch) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range matches {
		if seen[c.mf.AgentName] {
			continue
		}
		seen[c.mf.AgentName] = true
		out = append(out, c.mf.AgentName)
	}
	sort.Strings(out)
	return out
}

// identityMatch is a candidate session whose manifest matches the reconnect
// identity, kept with its directory name (the safe single-component id) for the
// lock/adopt operations.
type identityMatch struct {
	id string
	mf *Manifest
}

// tryReuse implements the F-27 reconnect, B-2 DEFAULT-RECLAIM variant: it finds
// sessions whose identity matches opts (agent-name + role + scope + team),
// most-recent first, and RECLAIMS the most-recent one — reusing its sessionId,
// inbox, processed, outbox and state, updating only PID + heartbeat (and
// backfilling a missing scope from opts), after revoking any previous listener
// so an orphan cannot keep consuming (F2/F3). Returns:
//
//   - (mf, release, nil)            reclaimed/resumed an existing session
//     (mf.LastReclaim reports what it superseded)
//   - (nil, nil, errReuseNoMatch)   no match -> caller registers fresh
//   - (nil, nil, err)               lock contention / IO failure
//
// B-2 inverts F-27: a live manifest PID is NO LONGER a reason to refuse. It
// proves only that a `listen` is alive, NOT that the Claude that owned it is —
// after a /clear the agent is gone but its background listen may still run as an
// orphan. The identity (agent-name+role+scope+team) + --resume IS the semantic
// claim to that session's continuity, so we reclaim it: revoke the previous
// listener (a new token via reclaimListenerLocked → the orphan's IsListenerCurrent
// goes false, it stops consuming) then adopt. Two SIMULTANEOUS --resume of the
// same identity is an operator error (one wins the lock; the other gets a
// contended error) — a deliberate second instance uses --force-new (which never
// enters tryReuse). The lock is held across revoke+adopt so they are atomic.
func (m *Manager) tryReuse(absProj string, opts RegisterOpts) (*Manifest, func() error, error) {
	matches, err := m.findIdentityMatches(absProj, opts)
	if err != nil {
		return nil, nil, err
	}
	if len(matches) == 0 {
		return nil, nil, errReuseNoMatch
	}

	// AMBIGUITY IS NOT A TIE TO BREAK. Dropping the name from the SEARCH was
	// right — a derived name is a function of the project path and discriminates
	// nothing — but it does not follow that the name stops meaning anything:
	//
	//	the name is not a search CRITERION      <- true, and why the filter went
	//	the name is still an IDENTITY marker    <- and this was lost with it
	//
	// Two sessions on one path with DIFFERENT names are two identities, created
	// through the supported path for exactly that (--force-new). Fusing them and
	// taking the most recent heartbeat picked an empty mailbox over one holding a
	// message — silently, and repeatably, which is the trap: an order that is
	// deterministic looks decided. Making a choice repeatable does not make it
	// right, and §2.2 of the mailbox design says that where duplicates exist the
	// system fails closed instead of choosing (CRI re-gate P1-1).
	//
	// Several records under ONE name are still one identity — that is the
	// ordinary post-compact case — so most-recent-first keeps applying there.
	if opts.AgentName == "" {
		if names := distinctNames(matches); len(names) > 1 {
			return nil, nil, fmt.Errorf("%w: %d sessions here answer to different names, so nothing was resumed — they are separate identities, not one: %s\n  say which one you are: --agent-name=<name>",
				ErrAmbiguousResume, len(matches), strings.Join(names, ", "))
		}
	}

	// The most-recent match is my identity's continuity (findIdentityMatches
	// sorts most-recent first). Reclaim it whether its PID is alive (orphan) or
	// dead (post-compact).
	c := matches[0]

	// THE NAME WE ARE ABOUT TO ADOPT HAS TO BE ONE THE REST OF THE TOOL ACCEPTS.
	//
	// Register validates opts.AgentName, which is empty on this path, so a
	// candidate carrying a pre-grammar name went straight through and came back
	// as `resumed` — and `tell` on that name then failed with "takes no flags",
	// because it never reaches a lookup (CRI re-gate P1-2).
	//
	// The part that makes it a defect rather than a gap: `join` REFUSES the same
	// session and offers the in-place repair. Two doors, opposite answers, one
	// state. This is the door I fixed second — the branch nobody named while the
	// other one was being closed.
	//
	// The remediation points at `join` on purpose: the repair already exists
	// there, in one place, and a second implementation of it here is how two
	// doors start disagreeing again.
	//
	// AND IT CARRIES --project-path. `register --resume --project-path=X` runs
	// from any cwd, but the suggested `join` without that flag falls back to the
	// CWD (join.go), so running the printed command from somewhere else
	// registered a NEW session there and left the legacy one — with its mail —
	// exactly where it was. The message promised "SAME id, SAME inbox" while
	// doing neither: a command that promises more than it performs, which is this
	// lot's own class one level up, written inside the error that repairs it.
	// Found by executing the printed command instead of reading it.
	//
	// KNOWN DEBT, deliberate: ProjectPath may contain a space, and this string is
	// meant to be pasted into a shell. Until lot 2 gives the tool a shell-safe
	// rendering, this command is not paste-safe for such a path — F-124's own
	// defect reproduced inside F-124's remediation. Carrying the target is the
	// P1; quoting it is lot 2, and it must not be quietly forgotten here.
	if verr := ValidateAgentName(c.mf.AgentName); verr != nil {
		return nil, nil, fmt.Errorf("%w: the session here is named %q and cannot be addressed — %v\n  repair it in place (SAME id, SAME inbox):\n    cab-bridge join --role=%s --agent-name=%s --project-path=%s",
			ErrUnaddressableResume, c.mf.AgentName, verr,
			defaultIfEmpty(opts.Role, RoleNeutral), SuggestAddressableName(c.mf.AgentName), c.mf.ProjectPath)
	}
	release, lerr := AcquireLock(filepath.Join(m.sessionDir(c.id), "lock"), false)
	if lerr != nil {
		if errors.Is(lerr, ErrLockHeld) {
			// Another register/reconnect is mid-claim on this very session: do not
			// race it into a duplicate (two simultaneous --resume of the same
			// identity is an operator error). --force-new for a 2nd instance.
			return nil, nil, fmt.Errorf("reuse: %s claim contended: %w (use --force-new for a deliberate second instance)", c.id, lerr)
		}
		return nil, nil, fmt.Errorf("reuse: lock %s: %w", c.id, lerr)
	}

	// Revoke the previous listener BEFORE adopting, under the lock we hold, so
	// revoke + adopt are one atomic critical section (a concurrent claim cannot
	// interleave). The orphan listener, at its next pre-move ownership check, sees
	// a token mismatch and stops consuming (B-2 fencing).
	reclaimInfo, rerr := m.reclaimListenerLocked(c.id)
	if rerr != nil {
		_ = release()
		return nil, nil, fmt.Errorf("reuse: revoke listener %s: %w", c.id, rerr)
	}
	mf, aerr := m.adoptAndBackfill(c.id, opts.Scope)
	if aerr != nil {
		_ = release()
		return nil, nil, fmt.Errorf("reuse: resume %s: %w", c.id, aerr)
	}
	ri := reclaimInfo
	mf.LastReclaim = &ri
	return mf, release, nil
}

// findIdentityMatches scans all session manifests and returns those matching the
// reconnect identity, sorted most-recent first (LastHeartbeat desc, then
// StartedAt desc, then id) for a deterministic multi-match resolution.
//
// Identity = effective role + scope + team + PROJECT PATH, plus the agent name
// ONLY when the caller supplied one (F-124; the long version is on wantAgent
// below). A resume with no name matches on where it is and what it does, and
// takes the name of whatever it finds — a derived name is a function of the
// project path, so as a criterion it adds nothing and as a constraint it ties
// re-entry to the version of that function.
//
// scopeMatch: equal non-empty scopes, OR a legacy candidate (empty scope) whose
// projectPath is an ancestor-or-equal of absProj.
//
// projectPath is part of the identity (§2.2, CRI diff-gate 1c P1-4). Without it
// two agents of the same name and role in different worktrees of ONE repo share
// a scope and match each other: a join from A would resume B's session — the
// most recent one wins — and the new life would adopt B's waiter and work on
// the WRONG MAILBOX while believing it started in A. A legacy candidate with no
// scope keeps the old ancestor rule, since it has no projectPath discipline to
// compare against.
func (m *Manager) findIdentityMatches(absProj string, opts RegisterOpts) ([]identityMatch, error) {
	// THE NAME IS PART OF THE IDENTITY ONLY WHEN THE CALLER SUPPLIED ONE.
	//
	// This line used to read `defaultIfEmpty(opts.AgentName, derivedAgentName(absProj))`,
	// and the history of that expression is worth keeping, because the fix that
	// produced it is what caused the defect that removed it.
	//
	// FIRST it was filepath.Base, while Register wrote a sanitised name: in a
	// directory called `feat@2` the writer stored `feat-2`, `--resume` looked for
	// `feat@2`, found nothing, and registered a SECOND session — the first one
	// keeping the mail. A writer's semantics changed and its readers were not
	// re-examined.
	//
	// The repair was to call derivedAgentName, the same function Register writes
	// with, and the comment concluded that naming the function is "the only
	// version of that sentence that cannot go stale". THAT IS TRUE WITHIN ONE
	// BINARY AND FALSE ACROSS TWO. It closes the divergence between a writer and
	// a reader compiled together, and opens the one between today's reader and
	// YESTERDAY'S WRITER — because now the identity depends on the current
	// definition of a function, so changing the derivation at all breaks re-entry
	// through an upgrade. F-124 changed it, and a session registered by the
	// previous binary was abandoned with its inbox.
	//
	// The same defect has a second face with no upgrade in it at all, which is
	// what showed the real cause: `register --agent-name=ESC-explicit` followed by
	// `register --resume` with NO name, same binary, same directory, produced two
	// sessions and left the mail in the first. There is no historic derivation to
	// recognise there — there is a name somebody chose. So "teach the reader the
	// old derivations" would have fixed neither this nor the next change.
	//
	// The cause is that a DERIVED name was ever used as a criterion. It is a
	// function of absProj, and absProj is already part of the identity below, so
	// it discriminates nothing — while binding re-entry to the version of a
	// function. When nobody asked for a name, the name is an OUTPUT of the resume,
	// adopted from the session we find, exactly as `join` already does with the
	// occupant it finds in a directory (join.go). Aligning register with join
	// rather than inventing a third rule.
	wantAgent := opts.AgentName // "" means: do not filter on the name at all
	wantRole := defaultIfEmpty(opts.Role, RoleNeutral)

	sessionsRoot := filepath.Join(m.DataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reuse: read sessions dir: %w", err)
	}

	var out []identityMatch
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, lerr := m.LoadManifest(e.Name())
		if lerr != nil {
			// A manifest that is not ours is skipped like an unreadable one, but
			// NOT in silence: suppressing it here turns the anomaly into a plain
			// "no session found", i.e. invisible in the very command someone
			// would use to look for it.
			_ = security.WarnNotOurs(e.Name(), lerr)
			continue
		}
		if wantAgent != "" && mf.AgentName != wantAgent {
			continue
		}
		if mf.Role != wantRole {
			continue
		}
		if opts.TeamID != "" && mf.TeamID != opts.TeamID {
			continue
		}
		if !scopeMatches(mf, opts.Scope, absProj) {
			continue
		}
		if mf.Scope != "" && filepath.Clean(mf.ProjectPath) != filepath.Clean(absProj) {
			continue
		}
		out = append(out, identityMatch{id: e.Name(), mf: mf})
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].mf, out[j].mf
		if !a.LastHeartbeat.Equal(b.LastHeartbeat) {
			return a.LastHeartbeat.After(b.LastHeartbeat)
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.After(b.StartedAt)
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// scopeMatches reports whether a candidate manifest's scope matches the
// reconnecting identity. Equal non-empty scopes match. A legacy candidate
// (empty scope, pre-F-17) matches when its projectPath is an ancestor-or-equal
// of absProj, so an empty scope never blocks a match but stays anchored to the
// project (retro-compat).
func scopeMatches(mf *Manifest, wantScope, absProj string) bool {
	if mf.Scope != "" {
		return mf.Scope == wantScope
	}
	return isPathDescendantOrEqual(absProj, mf.ProjectPath)
}

// adoptAndBackfill claims sessionID for the current process (PID + fresh
// heartbeat) and, if the session has no scope yet (legacy) and a scope is
// available, backfills it — a pre-F-17 session auto-upgrades to F-17 on resume
// (F-27). One RMW under manifestMu (same discipline as AdoptPID).
func (m *Manager) adoptAndBackfill(sessionID, scope string) (*Manifest, error) {
	m.manifestMu.Lock()
	defer m.manifestMu.Unlock()
	mf, err := m.LoadManifest(sessionID)
	if err != nil {
		return nil, err
	}
	mf.PID = os.Getpid()
	mf.LastHeartbeat = m.now()
	if mf.Scope == "" && scope != "" {
		mf.Scope = scope // F-27 backfill: legacy session adopts the F-17 scope
	}
	if err := m.SaveManifest(mf); err != nil {
		return nil, err
	}
	return mf, nil
}
