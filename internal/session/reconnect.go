package session

import (
	"errors"
	"fmt"
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
// Identity = effective agent-name + effective role + scope + team, where the
// effective agent-name/role apply the SAME defaults Register uses (so a resume
// with empty agent-name still matches a session registered with the basename
// default). scopeMatch: equal non-empty scopes, OR a legacy candidate (empty
// scope) whose projectPath is an ancestor-or-equal of absProj.
func (m *Manager) findIdentityMatches(absProj string, opts RegisterOpts) ([]identityMatch, error) {
	wantAgent := defaultIfEmpty(opts.AgentName, filepath.Base(absProj))
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
