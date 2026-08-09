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
//  1. Move inbox/, outbox/ and processed/ contents into
//     archive/<YYYY-MM-DD>/<sessionID>/<subdir>/ preserving filenames
//     (rename(2) atomic same-fs). AUDIT-1: all three subdirs are archived,
//     not just processed/, so unread inbox messages are never silently lost.
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

// ErrSessionBusy means a session was SKIPPED because it is locked or has a
// reply transaction in flight. It is not a failure of cleanup: a session
// mid-transaction is by definition not abandoned, and the next pass finds it
// again (CRI diff-gate P0-2).
var ErrSessionBusy = errors.New("cleanup: session busy")

// ErrOwnSessionRequired is returned when scope=my-session but OwnSessionID
// is empty. Caller must resolve the ID before calling Run.
var ErrOwnSessionRequired = errors.New("cleanup my-session: OwnSessionID required")

// Options bundle inputs for Run.
type Options struct {
	DataDir      string
	Scope        string
	OwnSessionID string
	// CallerScope is the project root the caller is standing in. scope=global
	// sweeps only sessions belonging to it — see globalSweep.
	CallerScope string
	// AllScopes restores the pre-v0.8 reach of scope=global: every session in the
	// data dir, other teams included. Explicit and deliberate, never a default.
	AllScopes     bool
	StaleSeconds  int
	RetentionDays int
	Now           func() time.Time // injection for tests
}

// SkippedOtherScopes counts stale sessions that scope=global would have removed
// before v0.8 and now leaves alone, because they belong to another project root.
// The caller MUST surface it: a semantic change that does not announce itself is
// the same defect as the behaviour being removed, pointed the other way — and
// whoever had a script expecting a full sweep gets a partial one with no error.

// Result summarizes what Run did. Useful for the cmd wrapper to print a
// human-readable summary and for tests to assert behavior precisely.
type Result struct {
	SessionsRemoved []string `json:"sessionsRemoved"`
	// RemovedByScope counts removals per project root. A bare list of ids says
	// WHAT was deleted and never WHOSE it was: with one data dir shared by
	// several teams, that is the difference between "I tidied up" and "I deleted
	// another team's work", and the id alone cannot tell them apart — not even
	// afterwards. Sessions with no scope are counted under "(no scope)".
	//
	// Additive, omitempty: `sessionsRemoved` keeps its exact shape, so scripts
	// parsing it are untouched.
	RemovedByScope map[string]int `json:"removedByScope,omitempty"`
	// SkippedOtherScopes: stale sessions left alone because they are another
	// team's. Zero unless scope=global without --all-scopes.
	SkippedOtherScopes int      `json:"skippedOtherScopes,omitempty"`
	ArchivesPurged     []string `json:"archivesPurged"`
	// PurgedSessionCount is how many archived SESSIONS went with those dates. A
	// date says when something was archived and nothing about how much of whose
	// work was inside it — and the retention sweep spans every project in the
	// data dir, so that number is the only thing standing between "removed 2
	// dates" and "removed two weeks of four teams' mailboxes".
	//
	// Additive, omitempty: archivesPurged keeps its shape.
	PurgedSessionCount int `json:"purgedSessionCount,omitempty"`
}

// scopeOfSession reads a session's scope BEFORE it is removed. "(no scope)" for
// legacy sessions and for anything unreadable — an unknown origin is still worth
// reporting, and a failure here must never abort a cleanup.
func scopeOfSession(dataDir, sessionID string) string {
	mgr := session.NewManager(dataDir, time.Second)
	mf, err := mgr.LoadManifest(sessionID)
	if err != nil || mf.Scope == "" {
		return "(no scope)"
	}
	return mf.Scope
}

// Run executes the cleanup pipeline. Returns a Result describing what was
// removed plus any error that aborted the run. Best-effort: per-session
// failures are skipped silently so a corrupt manifest cannot block cleanup
// of the rest.
func Run(_ context.Context, opts Options) (*Result, error) {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	// BUG-B: initialise slices to empty (not nil) so JSON output emits [] rather
	// than null, keeping `jq '... | length'` consumers from breaking.
	res := &Result{SessionsRemoved: []string{}, ArchivesPurged: []string{}, RemovedByScope: map[string]int{}}

	switch opts.Scope {
	case ScopeMySession:
		if opts.OwnSessionID == "" {
			return nil, ErrOwnSessionRequired
		}
		sc := scopeOfSession(opts.DataDir, opts.OwnSessionID)
		if err := archiveAndRemoveSession(opts.DataDir, opts.OwnSessionID, opts.Now); err != nil {
			return nil, fmt.Errorf("cleanup my-session: %w", err)
		}
		res.SessionsRemoved = append(res.SessionsRemoved, opts.OwnSessionID)
		res.RemovedByScope[sc]++

	case ScopeGlobal:
		removed, byScope, skipped, err := globalSweep(opts)
		if err != nil {
			return nil, err
		}
		res.SessionsRemoved = removed
		res.RemovedByScope = byScope
		res.SkippedOtherScopes = skipped

	default:
		return nil, fmt.Errorf("%w: %q (allowed: my-session, global)", ErrUnknownScope, opts.Scope)
	}

	// Retention sweep on archive/ (independent of scope — we always honor
	// the retention window for previously archived data).
	purged, sessionCount, err := purgeOldArchives(opts.DataDir, opts.RetentionDays, opts.Now)
	if err == nil {
		res.ArchivesPurged = purged
		res.PurgedSessionCount = sessionCount
	}
	return res, nil
}

// globalSweep scans every session under DataDir, archives + removes those that
// are stale per session.IsStale. Returns the IDs removed. BUG-8: StaleSeconds is
// the single source of truth shared with list-peers. F-23a: staleness goes
// through session.IsStale (same as peers/status), so a session in state
// "orchestrating" is heartbeat-exempt and is NOT swept here — the displayed
// "not stale" and the swept set can never diverge.
func globalSweep(opts Options) ([]string, map[string]int, int, error) {
	sessionsRoot := filepath.Join(opts.DataDir, "sessions")
	byScope := map[string]int{}
	skipped := 0
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []string{}, byScope, 0, nil // BUG-B: empty, not nil, for JSON []
		}
		return nil, nil, 0, fmt.Errorf("cleanup global: read sessions root: %w", err)
	}

	now := opts.Now()
	mgr := session.NewManager(opts.DataDir, time.Second) // interval irrelevant — we only LoadManifest

	removed := []string{} // BUG-B: empty, not nil, for JSON []
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := mgr.LoadManifest(e.Name())
		if err != nil {
			// Corrupt manifest — skip (Sprint 4 forensics will surface these).
			continue
		}
		if !session.IsStale(mf, opts.StaleSeconds, now) {
			continue // still fresh, or orchestrating (heartbeat-exempt, F-23a)
		}
		// The same ownership rule the auto-gc uses, from the same function: two
		// copies of one rule drift, and this codebase has already watched three
		// hand-kept copies of the role list disagree with each other.
		if !opts.AllScopes && !gcOwns(opts.CallerScope, mf.Scope) {
			skipped++
			continue
		}
		sc := mf.Scope
		if sc == "" {
			sc = "(no scope)"
		}
		if err := archiveAndRemoveSession(opts.DataDir, e.Name(), opts.Now); err != nil {
			continue // best-effort
		}
		removed = append(removed, e.Name())
		byScope[sc]++
	}
	return removed, byScope, skipped, nil
}

// messageSubdirs are the per-session subdirectories that hold message files
// and must be preserved before a session is deleted. ORDER is not significant
// (each is archived independently into its own archive subdir).
var messageSubdirs = []string{"inbox", "outbox", "processed"}

// archiveAndRemoveSession archives every message-bearing subdir (inbox/,
// outbox/, processed/) into archive/<date>/<sid>/<subdir>/ then RemoveAll-s
// the session dir. Failures inside the archive copy are silenced so a missing
// or empty subdir does not block delete (best-effort).
//
// AUDIT-1 fix: previously only processed/ was archived, so RemoveAll silently
// dropped any UNREAD inbox messages (and sent outbox copies) — reintroducing
// the Patil §1.6 "inbox loss on cleanup" pain this fork exists to kill. That
// loss is worse under auto-gc, where removal is automatic. Archiving all three
// subdirs closes the gap for every cleanup path (gc + my-session + global),
// since they all funnel through this helper.
//
// Subdir layout (vs a flat dir) keeps provenance explicit and removes any
// name-collision risk between a message that sits in inbox/ and a same-named
// copy in processed/.
// FENCING (CRI diff-gate P0-2): the whole decide-archive-remove runs while
// holding the session lock, and refuses outright if a reply transaction is in
// flight. Without it, cleanup could snapshot inbox/, a concurrent reply could
// link(2) its response in and report success, and the following RemoveAll would
// delete that brand-new file — leaving the ask CLOSED, the response gone from
// both inbox and archive, and the journal removed. No retry can repair that:
// it is a direct violation of the guarantee the transaction exists for.
//
// Refusing is right rather than merely delaying: a session mid-transaction is
// by definition not abandoned, and the next cleanup pass will find it again.
func archiveAndRemoveSession(dataDir, sid string, now func() time.Time) error {
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	dateDir := now().UTC().Format("2006-01-02")

	release, lerr := session.AcquireLock(filepath.Join(sessionDir, "lock"), false)
	if lerr != nil {
		return fmt.Errorf("%w: session %q is busy: %v", ErrSessionBusy, sid, lerr)
	}
	defer func() { _ = release() }()

	if _, err := os.Stat(filepath.Join(sessionDir, "reply-txn.json")); err == nil {
		return fmt.Errorf("%w: session %q has a reply transaction in flight", ErrSessionBusy, sid)
	}

	for _, sub := range messageSubdirs {
		srcDir := filepath.Join(sessionDir, sub)
		entries, err := os.ReadDir(srcDir)
		if err != nil || len(entries) == 0 {
			continue // missing or empty subdir — nothing to archive
		}
		archDir := filepath.Join(dataDir, "archive", dateDir, sid, sub)
		if err := os.MkdirAll(archDir, 0o700); err != nil {
			continue // best-effort: a failed archive must not block the rest
		}
		for _, e := range entries {
			if e.IsDir() {
				continue // message files only; no nested dirs expected
			}
			src := filepath.Join(srcDir, e.Name())
			dst := filepath.Join(archDir, e.Name())
			_ = os.Rename(src, dst) // best-effort, same-fs rename
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
func purgeOldArchives(dataDir string, retentionDays int, now func() time.Time) ([]string, int, error) {
	archRoot := filepath.Join(dataDir, "archive")
	sessionCount := 0
	entries, err := os.ReadDir(archRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []string{}, 0, nil // BUG-B: empty, not nil, for JSON []
		}
		return nil, 0, err
	}

	cutoff := now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	purged := []string{} // BUG-B: empty, not nil, for JSON []
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
		// Counted BEFORE the removal, for the obvious reason.
		inside := 0
		if sub, serr := os.ReadDir(dirPath); serr == nil {
			for _, d := range sub {
				if d.IsDir() {
					inside++
				}
			}
		}
		if err := os.RemoveAll(dirPath); err == nil {
			purged = append(purged, e.Name())
			sessionCount += inside
		}
	}
	return purged, sessionCount, nil
}
