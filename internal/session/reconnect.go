package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ErrIdentityLive is returned by Register (with Resume) when every session
// matching the requested identity is currently held by a live process: the
// session is not ours to take, and a silent duplicate must not be created. The
// caller surfaces it; --force-new creates a deliberate second instance.
var ErrIdentityLive = errors.New("a live session already exists with this identity")

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

// tryReuse implements the F-27 reconnect: it finds sessions whose identity
// matches opts (agent-name + role + scope + team), most-recent first, and
// resumes the first one NOT held by a live process — reusing its sessionId,
// inbox, processed, outbox and state, updating only PID + heartbeat (and
// backfilling a missing scope from opts). Returns:
//
//   - (mf, release, nil)            resumed an existing session
//   - (nil, nil, ErrIdentityLive)   matches exist but every one is live
//   - (nil, nil, errReuseNoMatch)   no match -> caller registers fresh
//
// Liveness is the manifest PID via IsProcessAlive — NOT the lock file. A running
// `listen` keeps the manifest PID alive (AdoptPID + heartbeat goroutine) but
// does NOT hold the lock (register acquires and immediately releases it), so the
// lock is free even while a session is live; the manifest PID is the correct
// "is the owner alive" signal, the same convention BUG-6 and the auto-gc use. A
// live owner is never stolen. The lock is then acquired only as the claim step,
// to serialize against a concurrent register/reconnect taking the same session.
func (m *Manager) tryReuse(absProj string, opts RegisterOpts) (*Manifest, func() error, error) {
	matches, err := m.findIdentityMatches(absProj, opts)
	if err != nil {
		return nil, nil, err
	}
	if len(matches) == 0 {
		return nil, nil, errReuseNoMatch
	}

	sawLive := false
	for _, c := range matches {
		if IsProcessAlive(c.mf.PID) {
			sawLive = true // a live process owns this session — never steal it
			continue
		}
		// Dead owner -> reusable. Acquire the lock to serialize against a
		// concurrent claim, then resume.
		release, lerr := AcquireLock(filepath.Join(m.sessionDir(c.id), "lock"), false)
		if lerr != nil {
			if errors.Is(lerr, ErrLockHeld) {
				sawLive = true // another register/reconnect is mid-claim — contended
			}
			continue
		}
		mf, aerr := m.adoptAndBackfill(c.id, opts.Scope)
		if aerr != nil {
			_ = release()
			return nil, nil, fmt.Errorf("reuse: resume %s: %w", c.id, aerr)
		}
		return mf, release, nil
	}

	if sawLive {
		return nil, nil, fmt.Errorf("%w: use --force-new for a second instance", ErrIdentityLive)
	}
	// Matches existed but none reusable (transient lock contention on all) —
	// safest is to register fresh.
	return nil, nil, errReuseNoMatch
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
