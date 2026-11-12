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
//     F-171: if anything cannot be archived the pipeline stops HERE, nothing is
//     removed, the error names the file and the next pass tries again.
//  2. Retire the session: its manifest first, then everything else, then the lock
//     file and the directory (archiveAndRemoveSession).
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

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
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
	SkippedOtherScopes int `json:"skippedOtherScopes,omitempty"`
	// SkippedBusy: stale sessions left alone because their lock is held or a reply
	// transaction is in flight (ErrSessionBusy). Not a failure: a session in the
	// middle of an operation is by definition not abandoned, and the next pass
	// finds it again. Additive, omitempty.
	SkippedBusy int `json:"skippedBusy,omitempty"`
	// SessionsFailed: sessions cleanup meant to remove and could not, with the
	// reason. Before F-160 every per-session error was swallowed as "best-effort"
	// and a lock error of ANY kind was reported as "busy", so a sweep that
	// failed looked exactly like one that had nothing to do. Additive, omitempty.
	SessionsFailed []SessionFailure `json:"sessionsFailed,omitempty"`
	ArchivesPurged []string         `json:"archivesPurged"`
	// PurgedSessionCount is how many archived SESSIONS went with those dates. A
	// date says when something was archived and nothing about how much of whose
	// work was inside it — and the retention sweep spans every project in the
	// data dir, so that number is the only thing standing between "removed 2
	// dates" and "removed two weeks of four teams' mailboxes".
	//
	// Additive, omitempty: archivesPurged keeps its shape.
	PurgedSessionCount int `json:"purgedSessionCount,omitempty"`
}

// SessionFailure names a session cleanup could not remove, and why.
type SessionFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// sweepOutcome is what globalSweep found, kept in one value so adding a counter
// does not add a return value to every caller.
type sweepOutcome struct {
	removed            []string
	byScope            map[string]int
	skippedOtherScopes int
	skippedBusy        int
	failed             []SessionFailure
}

// record files the error of one session's removal: busy is a skip, anything
// else is a failure the caller must be told about.
func (o *sweepOutcome) record(id string, err error) {
	switch {
	case errors.Is(err, ErrSessionBusy):
		o.skippedBusy++
	case errors.Is(err, session.ErrSessionGone):
		// Retired by somebody else between the listing and the lock: already done,
		// not a failure and not a removal of ours.
	default:
		o.failed = append(o.failed, SessionFailure{ID: id, Error: err.Error()})
	}
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
// removed plus any error that aborted the run. A session that cannot be retired
// does not abort a global sweep: it is reported in Result.SessionsFailed with
// the reason, and one whose manifest cannot be read is skipped and announced. For
// my-session the failure is the returned error.
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
		if err := archiveAndRemoveSession(opts.DataDir, opts.OwnSessionID, opts.Now, nil); err != nil {
			return nil, fmt.Errorf("cleanup my-session: %w", err)
		}
		res.SessionsRemoved = append(res.SessionsRemoved, opts.OwnSessionID)
		res.RemovedByScope[sc]++

	case ScopeGlobal:
		sw, err := globalSweep(opts)
		if err != nil {
			return nil, err
		}
		res.SessionsRemoved = sw.removed
		res.RemovedByScope = sw.byScope
		res.SkippedOtherScopes = sw.skippedOtherScopes
		res.SkippedBusy = sw.skippedBusy
		res.SessionsFailed = sw.failed

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
// are stale per session.IsStale. Returns what was removed, what was skipped
// (other scope, busy) and what failed, with the reason. BUG-8: StaleSeconds is
// the single source of truth shared with list-peers. F-23a: staleness goes
// through session.IsStale (same as peers/status), so a session in state
// "orchestrating" is heartbeat-exempt and is NOT swept here — the displayed
// "not stale" and the swept set can never diverge.
func globalSweep(opts Options) (sweepOutcome, error) {
	sessionsRoot := filepath.Join(opts.DataDir, "sessions")
	// BUG-B: removed is empty, not nil, for JSON [].
	out := sweepOutcome{removed: []string{}, byScope: map[string]int{}}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return sweepOutcome{}, fmt.Errorf("cleanup global: read sessions root: %w", err)
	}

	now := opts.Now()
	mgr := session.NewManager(opts.DataDir, time.Second) // interval irrelevant — we only LoadManifest

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := mgr.LoadManifest(e.Name())
		if err != nil {
			// Corrupt manifest — skip (Sprint 4 forensics will surface these).
			// A foreign one is announced: this command DELETES, so "I ignored
			// something I could not vouch for" is the minimum it owes the caller.
			_ = security.WarnNotOurs(e.Name(), err)
			continue
		}
		if !session.IsStale(mf, opts.StaleSeconds, now) {
			continue // still fresh, or orchestrating (heartbeat-exempt, F-23a)
		}
		// The same ownership rule the auto-gc uses, from the same function: two
		// copies of one rule drift, and this codebase has already watched three
		// hand-kept copies of the role list disagree with each other.
		if !opts.AllScopes && !gcOwns(opts.CallerScope, mf.Scope) {
			out.skippedOtherScopes++
			continue
		}
		sc := mf.Scope
		if sc == "" {
			sc = "(no scope)"
		}
		if err := archiveAndRemoveSession(opts.DataDir, e.Name(), opts.Now, nil); err != nil {
			out.record(e.Name(), err)
			continue
		}
		out.removed = append(out.removed, e.Name())
		out.byScope[sc]++
	}
	return out, nil
}

// messageSubdirs are the per-session subdirectories that hold message files
// and must be preserved before a session is deleted. ORDER is not significant
// (each is archived independently into its own archive subdir).
var messageSubdirs = []string{"inbox", "outbox", "processed"}

// archiveAndRemoveSession archives every message-bearing subdir (inbox/,
// outbox/, processed/) into archive/<date>/<sid>/<subdir>/ and then retires the
// session: its manifest goes first, then everything else, and last the lock file
// and the directory, after the lock is released.
//
// THE ARCHIVE IS NOT BEST-EFFORT (F-171). It used to be: a listing that failed, an archive
// folder that could not be created and a rename that was refused were all skipped, and the
// removal that followed deleted the message, so it was in neither the session nor the
// archive and nothing said so. Now the first error of the archive stops the retirement
// BEFORE the manifest: the session stays, nothing is deleted, the error names the file or
// the folder and says what to do, and the next cleanup pass tries again (archiveMessages, in
// archive.go, says what a stop leaves behind). A folder that does not exist, or is empty, is
// not an error: there is nothing to archive.
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
//
// FENCING (CRI diff-gate P0-2): the whole decide-archive-retire runs while
// holding the session lock, and refuses outright if a reply transaction is in
// flight. Without it, cleanup could snapshot inbox/, a concurrent reply could
// link(2) its response in and report success, and the following removal would
// delete that brand-new file — leaving the ask CLOSED, the response gone from
// both inbox and archive, and the journal removed. No retry can repair that:
// it is a direct violation of the guarantee the transaction exists for.
//
// Refusing is right rather than merely delaying: a session mid-transaction is
// by definition not abandoned, and the next cleanup pass will find it again.
//
// THE ORDER OF RETIREMENT IS THE POINT (F-160). A process that waits for the lock
// may have opened the lock file just before it is unlinked, and would then lock an
// inode nobody contends for any more. Two checks, both made by every session lock on
// the lock once taken, stop it: the file locked must be the one the name points to
// (session.checkNamed), and the manifest must exist. So the manifest is removed
// first, under the lock, before anything else and before the lock file; a late
// waiter then finds the session gone (session.ErrSessionGone) and writes nothing. The
// lock file is removed only after release, and the directory last, with a plain
// rmdir: a leftover nobody expected is an error returned to the caller, not
// something to delete blind.
//
// What this rests on: the manifest of a retired session does not come back. Every
// writer of an existing manifest takes the session lock first (updateManifest), and
// the two creators of a session folder, Register and the Patil import, create it
// exclusively, so nobody publishes a manifest into a folder that already exists.
//
// A LIMIT, NOT A GUARANTEE: the session lock is shared by every acquisition made
// inside one process (session.processHolds), so a retirement does not exclude a writer
// of the same session that runs in the same process. A retirement runs in two places
// in the binary: the cleanup command (Run), and the auto-gc that join and register
// start with (runAutoGC, then GCOrphans). The conclusion holds for both, and it holds
// for a reason that can stop being true: the auto-gc runs once, in sequence (a plain
// loop over the sessions, no goroutine), before the command has taken a session lock
// or started anything of its own, and it only retires sessions whose owner process
// is dead. If it is ever moved into a loop, or into a goroutine that runs beside
// something that writes sessions in the same process (a heartbeat, a listener), none
// of the checks above protects the writer.
//
// afterRelease is a test seam, nil in production: it runs after the lock is
// released and before the lock file and the directory are removed. The other seam,
// underLock, is in retireSeams.
func archiveAndRemoveSession(dataDir, sid string, now func() time.Time, afterRelease func()) error {
	return retireWithSeams(dataDir, sid, now, retireSeams{afterRelease: afterRelease})
}

// retireSeams are the test seams of the retirement, all nil in production.
type retireSeams struct {
	// underLock runs inside the section that holds the session lock, at its end: the
	// manifest and every other entry are gone, the lock file is not, and the lock has
	// not been released yet. It exists so a test can put a real process in contention
	// with the section that cleanup actually runs, not with a copy of it.
	underLock func()
	// afterRelease runs after the lock is released and before the lock file and the
	// directory are removed.
	afterRelease func()
	// readDir, mkdirAll and rename are the three calls the archive step makes on the disk
	// (archiveMessages). nil means the production one: os.ReadDir, os.MkdirAll and
	// transportfs.RenameAtomic. A test replaces one to make that call fail the way a
	// transient refusal does, which a permission on a folder cannot do (it refuses the
	// listing and the later removal together), and which on Windows has no permission to
	// stand on at all.
	readDir  func(string) ([]os.DirEntry, error)
	mkdirAll func(string, os.FileMode) error
	rename   func(oldpath, newpath string) error
}

func retireWithSeams(dataDir, sid string, now func() time.Time, seams retireSeams) error {
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	dateDir := now().UTC().Format("2006-01-02")

	mgr := session.NewManager(dataDir, time.Second) // only to take the lock
	release, lerr := mgr.AcquireSessionLock(sid, 0)
	if lerr != nil {
		// Only contention is "busy". Any other error (permissions, a missing
		// parent, a file where the lock should be) is a failure the caller must
		// hear about: before F-160 all of them were reported as busy and then
		// swallowed, so a sweep that could not touch anything looked idle.
		if errors.Is(lerr, session.ErrLockHeld) {
			return fmt.Errorf("%w: session %q is busy: %v", ErrSessionBusy, sid, lerr)
		}
		return fmt.Errorf("cleanup: session %q: acquire lock: %w", sid, lerr)
	}

	rerr := retireSession(dataDir, sid, sessionDir, dateDir, seams)
	if uerr := release(); uerr != nil {
		rerr = errors.Join(rerr, fmt.Errorf("cleanup: session %q: release lock: %w", sid, uerr))
	}
	if rerr != nil {
		return rerr
	}

	if seams.afterRelease != nil {
		seams.afterRelease()
	}
	return removeLockAndDir(sid, sessionDir)
}

// retireSession is the part of the removal that runs under the session lock: the
// reply-transaction check, the archive, the removal of the manifest and then of
// every other entry except the lock file.
//
// The archive comes before the manifest and is not optional: if it returns an error the
// retirement returns it at once, with the manifest, the lock file and every message that
// was not moved still in place (archiveMessages says what a stop leaves behind).
func retireSession(dataDir, sid, sessionDir, dateDir string, seams retireSeams) error {
	if _, err := os.Stat(filepath.Join(sessionDir, "reply-txn.json")); err == nil {
		return fmt.Errorf("%w: session %q has a reply transaction in flight", ErrSessionBusy, sid)
	}

	if err := archiveMessages(sid, sessionDir, filepath.Join(dataDir, "archive", dateDir, sid), seams); err != nil {
		return err
	}

	// The retirement itself: from here on every lock on this session is refused.
	if err := os.Remove(filepath.Join(sessionDir, "manifest.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cleanup: session %q: retire (remove manifest): %w", sid, err)
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return fmt.Errorf("cleanup: session %q: list before removal: %w", sid, err)
	}
	var failures []error
	for _, e := range entries {
		if e.Name() == session.LockFileName {
			continue // held by us until release; removed after it
		}
		if err := os.RemoveAll(filepath.Join(sessionDir, e.Name())); err != nil {
			failures = append(failures, fmt.Errorf("cleanup: session %q: remove %s: %w", sid, e.Name(), err))
		}
	}
	if seams.underLock != nil {
		seams.underLock()
	}
	return errors.Join(failures...)
}

// leftoverNote ends the error of a retirement that stopped halfway. The manifest is
// already gone, which is what retired the session, and every sweep lists sessions by
// their manifest: the folder that is left is not a session to any of them, so none
// will ever come back to it. "Holds no session any more" stays true only while nothing
// publishes a manifest into a folder that exists: Register and the Patil import create
// the folder exclusively for that reason (see archiveAndRemoveSession). If one of them
// stopped doing so, this sentence would be false and so would the advice it gives.
const leftoverNote = "the folder holds no session any more and no cleanup pass will collect it: it has to be removed by hand"

// removeLockAndDir is the last step, after the lock is released: the lock file,
// then the (by now empty) session directory with a non-recursive remove.
func removeLockAndDir(sid, sessionDir string) error {
	if err := os.Remove(filepath.Join(sessionDir, session.LockFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cleanup: session %q retired but its lock file could not be removed (%s): %w", sid, leftoverNote, err)
	}
	if err := os.Remove(sessionDir); err != nil {
		return fmt.Errorf("cleanup: session %q retired but its directory could not be removed (%s): %w", sid, leftoverNote, err)
	}
	return nil
}

// purgeOldArchives walks archive/ and removes any date-named subdir whose
// date is older than now - retentionDays. Returns the date strings purged
// for the Result summary. Non-date entries and parse failures are skipped.
// purgeOldArchives removes archive/<date>/ directories older than the retention
// window, and reports both the dates and how many archived sessions went with
// them.
//
// retentionDays <= 0 DISABLES the purge — it does not mean "purge everything",
// which is what a zero used to do: the cutoff became `now`, so every archive
// dated before this instant qualified. The value a user reaches for to say
// "don't purge" was the one that purged the most, and it was reachable —
// CAB_RETENTION_DAYS=0 passes envInt without a guard.
//
// Zero-means-disabled is not invented here: GCOrphans already treats gcHours<=0
// exactly this way, for the same defensive reason. One convention, two sweeps.
func purgeOldArchives(dataDir string, retentionDays int, now func() time.Time) ([]string, int, error) {
	if retentionDays <= 0 {
		return []string{}, 0, nil // disabled — never "purge everything"
	}
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
