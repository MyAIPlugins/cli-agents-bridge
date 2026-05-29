package cleanup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

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

// GCOrphans sweeps DataDir/sessions/ and removes sessions that are orphaned
// with certainty, archiving each one (processed/* into archive/<date>/<id>/)
// before deletion exactly like cleanup.Run — no silent data loss.
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
func GCOrphans(dataDir string, gcHours int, now func() time.Time) ([]Orphan, error) {
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
			continue // corrupt manifest — skip (cleanup --scope=global will surface it)
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
