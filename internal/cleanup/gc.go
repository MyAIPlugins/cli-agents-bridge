package cleanup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// Orphan describes a single session removed by GCOrphans. Returned (rather
// than logged inline) so the caller owns the I/O: GCOrphans stays free of
// stderr coupling and is asserted on its return value in tests, while the
// cmd wrapper (cmd/cab-bridge.runAutoGC) formats the human-readable log line
// "removed orphan session X (pid N dead, idle Hh)".
type Orphan struct {
	SessionID string
	PID       int
	IdleAge   time.Duration
}

// GCOrphans sweeps the CALLER'S SCOPE under DataDir/sessions/ and removes
// sessions that are orphaned with certainty, archiving each one (processed/*
// into archive/<date>/<id>/) before deletion exactly like cleanup.Run — no
// silent data loss.
//
// SCOPE, and why it is not optional. This runs on every `join` and every
// `register`: nobody asks for it, it is part of saying hello. Without the filter
// it read the whole data dir, so an agent entering one repository silently
// collected the abandoned sessions of every other team sharing
// ~/.claude/cli-agents-bridge — across the very boundary that isolates `peers`,
// name resolution and worktree pairing everywhere else. An explicit
// `cleanup --scope=global` is at least somebody's decision; this is not.
//
// Nothing is stranded by filtering: an orphan in another scope is collected by
// ITS OWN team's next join, which is the caller that should be doing it. The
// question was never whether to sweep, only who sweeps what.
//
// No override flag, for the reason `observer` has none: there is no case where
// somebody wants their own arrival to delete another team's work.
//
// The two empty-string cases are deliberate and opposite:
//
//   - a session with NO scope (legacy, pre-F-17) belongs to no team, so no team
//     can ever collect it. It is swept by whoever passes — otherwise the filter
//     would make exactly the abandoned sessions immortal.
//   - a CALLER with no scope (resolveScope failed) sweeps only those unowned
//     sessions and nothing else: not knowing where you are is not a licence to
//     tidy someone else's desk.
//
// "Orphaned with certainty" is the DOUBLE condition (LL-10), and both halves
// are load-bearing:
//
//   - owning PID is no longer alive (session.IsProcessAlive == false), AND
//   - lastHeartbeat is older than gcHours.
//
// The PID check alone is insufficient: a session that was just `register`-ed
// but is not yet inside `listen` already has a dead PID, because the one-shot
// register process exits the moment it returns (BUG-A). Only a stale heartbeat
// distinguishes "abandoned long ago" from "born seconds ago". Conversely a
// session inside `listen` keeps a live PID (via AdoptPID) and refreshes its
// heartbeat, so it is never swept even if gcHours is small.
//
// gcHours <= 0 is treated as "disabled" and returns no removals — a defensive
// echo of the caller's AutoGCHours>0 gate so GCOrphans cannot wipe everything
// if invoked with a zero threshold by mistake.
//
// Best-effort per session: a corrupt manifest or a failed archive/remove skips
// that session rather than aborting the whole sweep, mirroring globalSweep.
// The returned slice is always non-nil (empty, not nil) for clean JSON/length
// consumers, consistent with cleanup.Run (BUG-B).
func GCOrphans(dataDir, scope string, gcHours int, now func() time.Time) ([]Orphan, error) {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	removed := []Orphan{}
	if gcHours <= 0 {
		return removed, nil // disabled / defensive
	}

	sessionsRoot := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return removed, nil // nothing registered yet
		}
		return nil, fmt.Errorf("gc orphans: read sessions root: %w", err)
	}

	cutoff := now().Add(-time.Duration(gcHours) * time.Hour)
	mgr := session.NewManager(dataDir, time.Second) // interval irrelevant — LoadManifest only

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := mgr.LoadManifest(e.Name())
		if err != nil {
			// A manifest that is not ours is skipped like an unreadable one, but
			// NOT in silence: suppressing it here turns the anomaly into a plain
			// "no session found", i.e. invisible in the very command someone
			// would use to look for it.
			_ = security.WarnNotOurs(e.Name(), err)
			continue // corrupt manifest — skip (cleanup --scope=global will surface it)
		}
		if !gcOwns(scope, mf.Scope) {
			continue // another team's session — not this caller's to collect
		}
		if session.IsProcessAlive(mf.PID) {
			continue // live owner (e.g. in listen via AdoptPID) — never touch
		}
		if mf.LastHeartbeat.After(cutoff) {
			continue // heartbeat still fresh — possibly just registered, not abandoned
		}
		idle := now().Sub(mf.LastHeartbeat)
		if err := archiveAndRemoveSession(dataDir, e.Name(), now); err != nil {
			continue // best-effort: a single failure must not block the rest
		}
		removed = append(removed, Orphan{SessionID: e.Name(), PID: mf.PID, IdleAge: idle})
	}
	return removed, nil
}

// gcOwns reports whether a caller in callerScope may collect a session whose
// manifest carries sessionScope. See the scope paragraph on GCOrphans for the
// two empty-string cases.
func gcOwns(callerScope, sessionScope string) bool {
	if sessionScope == "" {
		return true // unowned: nobody else will ever collect it
	}
	return sessionScope == callerScope
}
