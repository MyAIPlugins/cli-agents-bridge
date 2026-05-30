package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// Manager owns the session lifecycle for cli-agents-bridge: session ID
// generation, manifest read/write, longest-prefix lookup, and heartbeat
// goroutine scheduling.
//
// Manager is intentionally stateless beyond its configuration — all session
// state lives on disk under DataDir. This keeps Manager safe to share across
// goroutines and trivially testable with a temp DataDir.
type Manager struct {
	// DataDir is the absolute path of the bridge state root, e.g.
	// ~/.claude/cli-agents-bridge/. Resolved from config.DataDir by the
	// caller (cmd/cab-bridge), never inferred here.
	DataDir string

	// HeartbeatInterval is the period at which the heartbeat goroutine
	// updates lastHeartbeat. Sourced from config.HeartbeatTickMs.
	HeartbeatInterval time.Duration

	// Now is the clock injection point for tests. Defaults to time.Now.
	Now func() time.Time

	// manifestMu serializes every read-modify-write of a manifest performed by
	// this Manager so concurrent goroutines in the same process cannot lose an
	// update. The motivating case (F-12): listen runs the heartbeat goroutine
	// (touchHeartbeat) and the consume loop (SetLastConsumed) concurrently; both
	// load-modify-save the SAME manifest, and AtomicWriteJSON is atomic only at
	// the file level (rename), not across the read-modify-write window — without
	// this lock one save would clobber the other's field. Guards ALL RMW methods
	// (touchHeartbeat, AdoptPID, SetLastConsumed) as defense in depth, so a
	// future RMW cannot silently reintroduce the lost-update bug. LoadManifest /
	// SaveManifest stay lock-free (they are the primitives called INSIDE the
	// guarded sections; locking them too would deadlock — sync.Mutex is not
	// reentrant).
	manifestMu sync.Mutex
}

// NewManager constructs a Manager with default clock.
func NewManager(dataDir string, heartbeatInterval time.Duration) *Manager {
	return &Manager{
		DataDir:           dataDir,
		HeartbeatInterval: heartbeatInterval,
		Now:               time.Now,
	}
}

// RegisterOpts is the input bundle for Register. ProjectPath is required;
// AgentName and Role default to safe values if empty (projectName basename
// and RoleNeutral respectively).
type RegisterOpts struct {
	ProjectPath  string
	AgentName    string
	Role         string
	ForceNew     bool
	Capabilities []string
}

// Register creates a new session: generates a session ID, writes manifest.json
// atomically, and acquires the PID lock. Returns the created Manifest plus a
// release function for the lock (caller must defer release()).
//
// BUG-6 fix: lock acquired via O_EXCL semantics in AcquireLock. If a session
// already exists for this ProjectPath (same .claude/bridge-session-style
// collision via longest-prefix-match), Register does NOT silently reuse it —
// the caller decides whether to resume or force a new ID via opts.ForceNew.
// Resume semantics are deferred to Sprint 2 (BUG-6 MVP scope is collision
// prevention, not resume UX).
func (m *Manager) Register(ctx context.Context, opts RegisterOpts) (*Manifest, func() error, error) {
	if opts.ProjectPath == "" {
		return nil, nil, errors.New("register: ProjectPath required")
	}
	absProj, err := filepath.Abs(opts.ProjectPath)
	if err != nil {
		return nil, nil, fmt.Errorf("register: resolve ProjectPath %q: %w", opts.ProjectPath, err)
	}

	// BUG-6 fix: prevent double-register on the same project path unless
	// caller explicitly opts out via ForceNew. We detect "same project" via
	// LongestPrefixLookup returning an exact-match manifest whose PID is
	// still alive. Unlike Patil's design (which reused .claude/bridge-session
	// silently and produced duplicate IDs sharing inbox/outbox), we refuse
	// the second register and surface the conflict.
	if !opts.ForceNew {
		if existingID, lerr := m.LongestPrefixLookup(absProj); lerr == nil {
			if existing, merr := m.LoadManifest(existingID); merr == nil &&
				filepath.Clean(existing.ProjectPath) == absProj &&
				IsProcessAlive(existing.PID) {
				return nil, nil, fmt.Errorf("%w: project %q already has active session %s (pid %d), use --force-new to override",
					ErrSessionExistsForProject, absProj, existingID, existing.PID)
			}
		}
	}

	sessionID, err := generateSessionID()
	if err != nil {
		return nil, nil, fmt.Errorf("register: %w", err)
	}

	sessionDir := m.sessionDir(sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("register: mkdir session dir %q: %w", sessionDir, err)
	}
	for _, sub := range []string{"inbox", "outbox"} {
		if err := os.MkdirAll(filepath.Join(sessionDir, sub), 0o700); err != nil {
			return nil, nil, fmt.Errorf("register: mkdir %s: %w", sub, err)
		}
	}
	// SC-2 explicit chmod for pre-existing dirs (MkdirAll honors umask but
	// does not chmod existing dirs back down).
	_ = os.Chmod(sessionDir, 0o700)

	release, err := AcquireLock(filepath.Join(sessionDir, "lock"), opts.ForceNew)
	if err != nil {
		// Failed lock: rollback the session dir we just created to avoid
		// leaving an orphan. Best-effort.
		_ = os.RemoveAll(sessionDir)
		return nil, nil, fmt.Errorf("register: %w", err)
	}

	now := m.now()
	manifest := &Manifest{
		SessionID:     sessionID,
		SchemaVersion: SchemaVersionV2,
		ProjectName:   filepath.Base(absProj),
		ProjectPath:   absProj,
		AgentName:     defaultIfEmpty(opts.AgentName, filepath.Base(absProj)),
		Role:          defaultIfEmpty(opts.Role, RoleNeutral),
		PID:           os.Getpid(),
		StartedAt:     now,
		LastHeartbeat: now,
		Status:        StatusActive,
		Capabilities:  defaultCapabilities(opts.Capabilities),
	}

	if err := m.SaveManifest(manifest); err != nil {
		_ = release()
		_ = os.RemoveAll(sessionDir)
		return nil, nil, fmt.Errorf("register: %w", err)
	}

	return manifest, release, nil
}

// SaveManifest atomically writes manifest.json under the session dir.
// SC-5 enforced via transportfs.AtomicWriteJSON.
func (m *Manager) SaveManifest(manifest *Manifest) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("savemanifest: %w", err)
	}
	path := filepath.Join(m.sessionDir(manifest.SessionID), "manifest.json")
	if err := transportfs.AtomicWriteJSON(path, manifest); err != nil {
		return fmt.Errorf("savemanifest: %w", err)
	}
	return nil
}

// LoadManifest reads manifest.json for sessionID. Returns Manifest with v1
// defaults applied if the on-disk schema is v1 (PLAN §4.3 backward-compat
// read).
//
// Callers should perform security.CheckOwnership(path) before LoadManifest
// when the session ID is not their own (SC-3 layered defense — Manager does
// not enforce ownership to keep it composable with internal/security).
func (m *Manager) LoadManifest(sessionID string) (*Manifest, error) {
	path := filepath.Join(m.sessionDir(sessionID), "manifest.json")
	var manifest Manifest
	if err := transportfs.ReadJSON(path, &manifest); err != nil {
		return nil, fmt.Errorf("loadmanifest %s: %w", sessionID, err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("loadmanifest %s: %w", sessionID, err)
	}
	if manifest.SchemaVersion == 1 {
		manifest.ApplyV1Defaults()
	}
	return &manifest, nil
}

// LongestPrefixLookup scans all session manifests under DataDir and returns
// the session ID whose ProjectPath is the longest prefix of cwd. Resolves
// BUG-5 (Patil's get-session-id.sh exited at first match without prefix
// length tracking, returning non-deterministic results for nested cwds).
//
// Returns ErrNoSessionForCwd when no manifest's ProjectPath matches cwd or
// any of its ancestors.
//
// cwd is resolved via filepath.Abs before comparison; manifest ProjectPath
// is already absolute by Register's contract.
func (m *Manager) LongestPrefixLookup(cwd string) (string, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("longestprefix: resolve cwd %q: %w", cwd, err)
	}

	sessionsRoot := filepath.Join(m.DataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNoSessionForCwd
		}
		return "", fmt.Errorf("longestprefix: read sessions dir %q: %w", sessionsRoot, err)
	}

	bestMatch := ""
	bestLen := -1
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := m.LoadManifest(e.Name())
		if err != nil {
			// Corrupt manifest — skip silently. A noisy log is reserved for
			// the cleanup subcommand which has a clearer mandate.
			continue
		}
		if !isPathDescendantOrEqual(absCwd, mf.ProjectPath) {
			continue
		}
		if len(mf.ProjectPath) > bestLen {
			bestLen = len(mf.ProjectPath)
			// NEW-1: return the directory name (the real, single-component
			// identity we just loaded by) rather than mf.SessionID, which is
			// an attacker-influenceable manifest field. Prevents a crafted
			// sessionId from flowing out as an unvalidated path component.
			bestMatch = e.Name()
		}
	}

	if bestMatch == "" {
		return "", ErrNoSessionForCwd
	}
	return bestMatch, nil
}

// ErrNoSessionForCwd is returned by LongestPrefixLookup when no manifest's
// ProjectPath matches the given cwd.
var ErrNoSessionForCwd = errors.New("no session matches cwd or its ancestors")

// ErrSessionExistsForProject is returned by Register when a live session
// already exists for the given ProjectPath. Override with RegisterOpts.ForceNew
// (passed through from --force-new CLI flag). BUG-6 fix per PLAN §4.5.
var ErrSessionExistsForProject = errors.New("session already exists for project")

// StartHeartbeat launches a goroutine that updates manifest.lastHeartbeat
// every m.HeartbeatInterval. The goroutine exits when ctx is canceled.
//
// BUG-1 fix: heartbeat is scheduled by Manager itself, not externally —
// resolves the structural gap in Patil's bridge-listen.sh which expected
// some other script to call heartbeat.sh during the polling loop.
//
// Errors during update are silently retried at the next tick (heartbeat is
// best-effort lifecycle metadata). To surface persistent failures, the
// caller can wrap StartHeartbeat with its own retry+alert logic — but for
// MVP we keep it minimal.
//
// The returned channel is closed when the goroutine exits, allowing the
// caller to wait for clean shutdown after ctx cancellation.
func (m *Manager) StartHeartbeat(ctx context.Context, sessionID string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(m.HeartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.touchHeartbeat(sessionID)
			}
		}
	}()
	return done
}

// touchHeartbeat reads manifest, sets LastHeartbeat = now, atomic-writes back.
// Internal; the public surface is StartHeartbeat for the goroutine path
// and Touch for the single-shot path. Holds manifestMu for the whole
// read-modify-write (see the field doc) so a concurrent SetLastConsumed cannot
// lose its update.
func (m *Manager) touchHeartbeat(sessionID string) error {
	m.manifestMu.Lock()
	defer m.manifestMu.Unlock()
	manifest, err := m.LoadManifest(sessionID)
	if err != nil {
		return err
	}
	manifest.LastHeartbeat = m.now()
	return m.SaveManifest(manifest)
}

// SetLastConsumed records msgID as the most recently consumed inbox message in
// sessionID's manifest (F-12 observability). Called by listen after emitting a
// message and by receive after matching a reply. Holds manifestMu for the whole
// read-modify-write so the concurrent heartbeat goroutine cannot clobber the
// field (see the manifestMu doc).
func (m *Manager) SetLastConsumed(sessionID, msgID string) error {
	m.manifestMu.Lock()
	defer m.manifestMu.Unlock()
	manifest, err := m.LoadManifest(sessionID)
	if err != nil {
		return err
	}
	manifest.LastConsumedMsgID = msgID
	return m.SaveManifest(manifest)
}

// Touch refreshes the lastHeartbeat field of sessionID's manifest to "now"
// without launching a goroutine. Used by the connect-peer subcommand to
// guarantee the sender's heartbeat is fresh at handshake time (BUG-9 fix:
// Patil's connect-peer.sh did not refresh sender heartbeat, so a long-idle
// peer could appear stale to the remote at the very moment of connect).
//
// Returns the underlying load/save errors if either fails — caller decides
// whether to abort the connect attempt.
func (m *Manager) Touch(sessionID string) error {
	return m.touchHeartbeat(sessionID)
}

// AdoptPID claims sessionID for the current process by writing its PID into the
// manifest (and refreshing the heartbeat). The long-running listen command
// calls this at startup so collision detection (BUG-6) and stale detection
// observe a LIVE owner — unlike the one-shot register command, whose PID dies
// the moment it returns (Sprint 6 BUG-A). Touch deliberately does NOT adopt the
// PID: connect.go calls Touch from a short-lived process whose PID would be
// just as ephemeral as register's, so only a real listen owner takes ownership.
func (m *Manager) AdoptPID(sessionID string) error {
	m.manifestMu.Lock()
	defer m.manifestMu.Unlock()
	manifest, err := m.LoadManifest(sessionID)
	if err != nil {
		return err
	}
	manifest.PID = os.Getpid()
	manifest.LastHeartbeat = m.now()
	return m.SaveManifest(manifest)
}

// sessionDir returns the absolute filesystem path of the per-session
// directory under DataDir. Does not validate sessionID — callers must do so
// via security.ValidateSessionID before passing untrusted input (SC-4).
func (m *Manager) sessionDir(sessionID string) string {
	return filepath.Join(m.DataDir, "sessions", sessionID)
}

// now returns the current time via the injected clock (defaults to time.Now).
func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

// generateSessionID returns 8 lowercase hex chars (4 bytes entropy = 2^32
// possibilities — collision probability for ~10K sessions is ~0.01%, far
// beyond realistic single-user single-machine workload). Always satisfies
// SC-4 regex ^[a-z0-9]{6,32}$.
func generateSessionID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// isPathDescendantOrEqual returns true if child is the same path as parent,
// or is a directory descendant of parent. Both are expected to be absolute
// and Clean()-ed. We compare with a trailing-separator suffix to avoid the
// classic /foo/barbaz matching /foo/bar bug.
func isPathDescendantOrEqual(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// defaultIfEmpty returns fallback when s is empty, otherwise s.
func defaultIfEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// defaultCapabilities returns the MVP default capability set if caps is nil
// or empty. Capabilities are an informational manifest field; routing does
// not consult them yet (Sprint 2+).
func defaultCapabilities(caps []string) []string {
	if len(caps) == 0 {
		return []string{"query", "context-dump", "conversation"}
	}
	return caps
}
