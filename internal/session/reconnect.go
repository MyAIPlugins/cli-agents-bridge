package session

import (
	"errors"
	"fmt"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"os"
	"path/filepath"
	"sort"
)

// errReuseNoMatch is the internal sentinel meaning "no session matched the
// identity" — Register treats it as "fall through to a fresh register" (the
// idempotent reconnect-or-register behaviour). Never surfaced to callers.
var errReuseNoMatch = errors.New("reuse: no matching session")

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

	// The most-recent match is my identity's continuity (findIdentityMatches
	// sorts most-recent first). Reclaim it whether its PID is alive (orphan) or
	// dead (post-compact).
	c := matches[0]
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
// Identity = effective agent-name + effective role + scope + team + PROJECT
// PATH, where the effective agent-name/role come from the SAME FUNCTIONS
// Register writes with — derivedAgentName, not a second copy of the rule — so a
// resume with an empty agent-name still matches a session registered with the
// default. It said "the same defaults" and was true until the two drifted
// apart; naming the function is the only form of that claim that cannot go
// stale. scopeMatch: equal non-empty scopes, OR a legacy
// candidate (empty scope) whose projectPath is an ancestor-or-equal of absProj.
//
// projectPath is part of the identity (§2.2, CRI diff-gate 1c P1-4). Without it
// two agents of the same name and role in different worktrees of ONE repo share
// a scope and match each other: a join from A would resume B's session — the
// most recent one wins — and the new life would adopt B's waiter and work on
// the WRONG MAILBOX while believing it started in A. A legacy candidate with no
// scope keeps the old ancestor rule, since it has no projectPath discipline to
// compare against.
func (m *Manager) findIdentityMatches(absProj string, opts RegisterOpts) ([]identityMatch, error) {
	// derivedAgentName, the SAME function Register writes with — not
	// filepath.Base, which is what this line said until the two diverged.
	//
	// F-116 made Register sanitise the derived default, and this reader kept the
	// raw basename: in a directory called `feat@2` the writer stored `feat-2` and
	// `--resume` went looking for `feat@2`, found nothing, and registered a
	// SECOND session. If the first held mail, the peer came back to an empty
	// inbox with its work in the other session. Reproduced on the binary, two
	// distinct ids.
	//
	// The canonical shape: a writer's semantics changed and its readers were not
	// re-examined. The comment above claimed the two applied "the SAME defaults
	// Register uses" — true when written, false the moment the sanitisation
	// landed, and it is why nobody went looking. Calling the same function is the
	// only version of that sentence that cannot go stale.
	wantAgent := defaultIfEmpty(opts.AgentName, derivedAgentName(absProj))
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
		if mf.AgentName != wantAgent || mf.Role != wantRole {
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
