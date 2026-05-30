package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// loadConfigOrFail loads runtime config, prints warnings to stderr, and runs
// the SC-7 base-dir integrity check before any session file is touched.
// Returns the resolved config; on load or integrity failure returns a wrapped
// error the caller surfaces with exit 1.
func loadConfigOrFail() (config.Config, error) {
	cfg, warnings, err := config.Load()
	if err != nil {
		return cfg, fmt.Errorf("load config: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "config warning:", w)
	}
	if err := bootstrapDataDir(cfg.DataDir); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// bootstrapDataDir performs the SC-7 base-directory integrity check (PLAN §9,
// FINDING-1). It runs on every subcommand via loadConfigOrFail, before any
// session file is touched, on the already-absolute DataDir (FINDING-11).
//
// Sequence (OpenSSH safe_path / gpg-agent pattern, security audit R2):
//   - missing dir        → first run: create it 0o700, return nil (NOT an attack)
//   - symlink            → FATAL: never auto-repair, a symlink is intentional (TM-5)
//   - not a directory    → FATAL
//   - owner != us        → FATAL: cannot chown without root, structural tamper
//   - perms & 0o077 != 0 → WARN + chmod 0o700 (safe auto-repair, like gpg-agent)
//
// Running as root (Getuid()==0): the owner check is skipped with a stderr
// warning, consistent with security.CheckOwnership.
func bootstrapDataDir(dataDir string) error {
	info, err := os.Lstat(dataDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// First run: create the base dir. umask 077 (SC-1) plus the
			// explicit mode yields 0o700.
			if mkErr := os.MkdirAll(dataDir, 0o700); mkErr != nil {
				return fmt.Errorf("create data dir %q: %w", dataDir, mkErr)
			}
			return nil
		}
		return fmt.Errorf("stat data dir %q: %w", dataDir, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data dir %q is a symlink: refusing to operate (possible symlink attack, TM-5)", dataDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("data dir %q exists but is not a directory", dataDir)
	}

	if os.Getuid() == 0 {
		fmt.Fprintf(os.Stderr, "cab-bridge: running as root, data dir ownership check skipped for %q\n", dataDir)
	} else {
		sys, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("data dir %q: ownership check unsupported on this platform", dataDir)
		}
		if int(sys.Uid) != os.Getuid() {
			return fmt.Errorf("data dir %q owned by uid %d, expected current uid %d: refusing to operate", dataDir, sys.Uid, os.Getuid())
		}
	}

	if info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "cab-bridge: data dir %q has loose perms %04o, tightening to 0700\n", dataDir, info.Mode().Perm())
		if err := os.Chmod(dataDir, 0o700); err != nil {
			return fmt.Errorf("tighten data dir %q perms to 0700: %w", dataDir, err)
		}
	}
	return nil
}

// newSessionManager builds a session.Manager from a loaded config. All
// subcommands that touch sessions share this constructor for consistency.
func newSessionManager(cfg config.Config) *session.Manager {
	return session.NewManager(cfg.DataDir, time.Duration(cfg.HeartbeatTickMs)*time.Millisecond)
}

// runAutoGC sweeps orphan sessions (v0.2.1, F10) and logs each removal to logw.
// It is the cmd-side glue around cleanup.GCOrphans: the library returns the
// removed sessions, this wrapper owns the explicit stderr log so the cleanup
// is observable and never silent (anti-pattern AP-fork-2: hidden cleanup as a
// side effect). Disabled when cfg.AutoGCHours <= 0.
//
// Failures are non-fatal by design: a broken gc pass must never block the
// register/listen the user actually asked for, so the error is logged and the
// caller proceeds. Returns the removed orphans (nil when disabled) so callers
// and tests can inspect what was swept.
func runAutoGC(cfg config.Config, logw io.Writer) []cleanup.Orphan {
	if cfg.AutoGCHours <= 0 {
		return nil
	}
	removed, err := cleanup.GCOrphans(cfg.DataDir, cfg.AutoGCHours, nil)
	if err != nil {
		fmt.Fprintf(logw, "cab-bridge: auto-gc failed (non-fatal): %v\n", err)
		return nil
	}
	for _, o := range removed {
		fmt.Fprintf(logw, "cab-bridge: auto-gc removed orphan session %s (pid %d dead, idle %s)\n",
			o.SessionID, o.PID, o.IdleAge.Round(time.Hour))
	}
	return removed
}

// resolveSessionID returns the session ID to operate on. If flagValue is
// non-empty it is validated via SC-4 and returned. Otherwise the function
// looks up the longest-prefix-match for the current working directory and
// returns that ID. Returns a wrapped error suitable for stderr+exit on
// any failure.
func resolveSessionID(mgr *session.Manager, flagValue string) (string, error) {
	if flagValue != "" {
		if err := security.ValidateSessionID(flagValue); err != nil {
			return "", err
		}
		return flagValue, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd for session lookup: %w", err)
	}
	sid, err := mgr.LongestPrefixLookup(cwd)
	if err != nil {
		if errors.Is(err, session.ErrNoSessionForCwd) {
			return "", fmt.Errorf("no session found for cwd %q — register first with `cab-bridge register` or pass --session-id", cwd)
		}
		return "", fmt.Errorf("session lookup from cwd %q: %w", cwd, err)
	}
	// Defensive SC-4 re-validation (consistent with receive.go). LongestPrefixLookup
	// now returns the directory name (NEW-1), already safe, but validating here
	// keeps the contract uniform across all session-resolution paths.
	if err := security.ValidateSessionID(sid); err != nil {
		return "", fmt.Errorf("session lookup returned invalid id %q: %w", sid, err)
	}
	return sid, nil
}

// resolveScope derives the F-17 project-root scope for path: it resolves $HOME
// and calls session.FindProjectRoot. Both steps are non-fatal — scope is a
// convenience filter that must NEVER block register or peers — so any failure is
// logged to stderr and the function returns "" (the "no scope / show-all"
// sentinel). This is the single EXPLICIT, documented, logged scope fallback;
// there is no silent degradation. A missing $HOME only disables the dotfiles
// exclusion (FindProjectRoot tolerates an empty home).
func resolveScope(path string) string {
	home, herr := os.UserHomeDir()
	if herr != nil {
		fmt.Fprintf(os.Stderr, "cab-bridge: cannot resolve home for scope detection (non-fatal): %v\n", herr)
		home = ""
	}
	scope, serr := session.FindProjectRoot(path, home)
	if serr != nil {
		fmt.Fprintf(os.Stderr, "cab-bridge: scope detection failed for %q (non-fatal): %v — proceeding without scope\n", path, serr)
		return ""
	}
	return scope
}
