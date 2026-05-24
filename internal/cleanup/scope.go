// Package cleanup implements scope-aware session cleanup with archive
// + retention sweep. Resolves BUG-4 (Patil cleanup.sh globally wiped any
// stale session across projects, evidenced by chatterence-bi-template
// cleanup destroying ac-agents sessions on 2026-05-24).
//
// Two scopes:
//   - "my-session" (default): act only on the caller's own session ID.
//     Other projects' sessions are never touched.
//   - "global": act on every stale session in DataDir. The cmd wrapper
//     enforces an interactive confirmation (TTY) or --force flag —
//     this library accepts a boolean and assumes the caller already
//     gated the prompt.
//
// Cleanup pipeline per session:
//  1. Move processed/* into archive/<YYYY-MM-DD>/<sessionID>/ preserving
//     filenames (rename(2) atomic same-fs).
//  2. RemoveAll session dir (sessions/<sessionID>/).
//
// After per-session pass, retention sweep removes archive/<YYYY-MM-DD>/
// directories older than RetentionDays (default 7, GDPR-1 data
// minimization, PLAN §9.6).
//
// StaleSeconds is sourced from config.Config.StaleSeconds (BUG-8 fix:
// list-peers and cleanup share the same field — no separate hardcoded
// values diverging silently across scripts).
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// Scope constants. cmd wrapper validates user input against these.
const (
	ScopeMySession = "my-session"
	ScopeGlobal    = "global"
)

// ErrUnknownScope is returned by Run for any scope outside the constants.
var ErrUnknownScope = errors.New("unknown cleanup scope")

// ErrOwnSessionRequired is returned when scope=my-session but OwnSessionID
// is empty. Caller must resolve the ID before calling Run.
var ErrOwnSessionRequired = errors.New("cleanup my-session: OwnSessionID required")

// Options bundle inputs for Run.
type Options struct {
	DataDir       string
	Scope         string
	OwnSessionID  string
	StaleSeconds  int
	RetentionDays int
	Now           func() time.Time // injection for tests
}

// Result summarizes what Run did. Useful for the cmd wrapper to print a
// human-readable summary and for tests to assert behavior precisely.
type Result struct {
	SessionsRemoved []string `json:"sessionsRemoved"`
	ArchivesPurged  []string `json:"archivesPurged"`
}

// Run executes the cleanup pipeline. Returns a Result describing what was
// removed plus any error that aborted the run. Best-effort: per-session
// failures are skipped silently so a corrupt manifest cannot block cleanup
// of the rest.
func Run(_ context.Context, opts Options) (*Result, error) {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	res := &Result{}

	switch opts.Scope {
	case ScopeMySession:
		if opts.OwnSessionID == "" {
			return nil, ErrOwnSessionRequired
		}
		if err := archiveAndRemoveSession(opts.DataDir, opts.OwnSessionID, opts.Now); err != nil {
			return nil, fmt.Errorf("cleanup my-session: %w", err)
		}
		res.SessionsRemoved = append(res.SessionsRemoved, opts.OwnSessionID)

	case ScopeGlobal:
		removed, err := globalSweep(opts)
		if err != nil {
			return nil, err
		}
		res.SessionsRemoved = removed

	default:
		return nil, fmt.Errorf("%w: %q (allowed: my-session, global)", ErrUnknownScope, opts.Scope)
	}

	// Retention sweep on archive/ (independent of scope — we always honor
	// the retention window for previously archived data).
	purged, err := purgeOldArchives(opts.DataDir, opts.RetentionDays, opts.Now)
	if err == nil {
		res.ArchivesPurged = purged
	}
	return res, nil
}

// globalSweep scans every session under DataDir, archives + removes those
// whose lastHeartbeat is older than StaleSeconds. Returns the IDs removed.
// BUG-8: StaleSeconds is the single source of truth shared with list-peers.
func globalSweep(opts Options) ([]string, error) {
	sessionsRoot := filepath.Join(opts.DataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cleanup global: read sessions root: %w", err)
	}

	cutoff := opts.Now().Add(-time.Duration(opts.StaleSeconds) * time.Second)
	mgr := session.NewManager(opts.DataDir, time.Second) // interval irrelevant — we only LoadManifest

	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := mgr.LoadManifest(e.Name())
		if err != nil {
			// Corrupt manifest — skip (Sprint 4 forensics will surface these).
			continue
		}
		if mf.LastHeartbeat.After(cutoff) {
			continue // still fresh
		}
		if err := archiveAndRemoveSession(opts.DataDir, e.Name(), opts.Now); err != nil {
			continue // best-effort
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

// archiveAndRemoveSession moves processed/* into archive/<date>/<sid>/
// then RemoveAll-s the session dir. Failures inside the archive copy are
// silenced so a missing/empty processed/ does not block delete.
func archiveAndRemoveSession(dataDir, sid string, now func() time.Time) error {
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	processedDir := filepath.Join(sessionDir, "processed")

	if entries, err := os.ReadDir(processedDir); err == nil && len(entries) > 0 {
		dateDir := now().UTC().Format("2006-01-02")
		archDir := filepath.Join(dataDir, "archive", dateDir, sid)
		if err := os.MkdirAll(archDir, 0o700); err == nil {
			for _, e := range entries {
				src := filepath.Join(processedDir, e.Name())
				dst := filepath.Join(archDir, e.Name())
				_ = os.Rename(src, dst) // best-effort
			}
		}
	}

	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("remove session %q: %w", sid, err)
	}
	return nil
}

// purgeOldArchives walks archive/ and removes any date-named subdir whose
// date is older than now - retentionDays. Returns the date strings purged
// for the Result summary. Non-date entries and parse failures are skipped.
func purgeOldArchives(dataDir string, retentionDays int, now func() time.Time) ([]string, error) {
	archRoot := filepath.Join(dataDir, "archive")
	entries, err := os.ReadDir(archRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	cutoff := now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	var purged []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d, err := time.Parse("2006-01-02", e.Name())
		if err != nil {
			continue
		}
		if !d.Before(cutoff) {
			continue
		}
		dirPath := filepath.Join(archRoot, e.Name())
		if err := os.RemoveAll(dirPath); err == nil {
			purged = append(purged, e.Name())
		}
	}
	return purged, nil
}
