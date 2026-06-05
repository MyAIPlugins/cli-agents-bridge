package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// resolveScope derives the F-17 project-root scope for path: it resolves $HOME,
// calls session.FindProjectRoot (a lexical walk), then symlink-canonicalizes the
// result. Every step is non-fatal — scope is a convenience filter that must
// NEVER block register or peers — so any failure is logged to stderr and the
// function returns "" (the "no scope / show-all" sentinel) or the un-resolved
// path. This is the single EXPLICIT, documented, logged scope path; there is no
// silent degradation. A missing $HOME only disables the dotfiles exclusion
// (FindProjectRoot tolerates an empty home).
//
// F-41 symlink canonicalization: FindProjectRoot is lexical, but its two branches
// disagree under a symlinked path — a normal/main repo (.git DIR) keeps the
// lexical cwd path, while a linked worktree (.git FILE) inherits the `gitdir:`
// that git writes ALREADY symlink-resolved. collectPeers compares scope as a
// STRING, so on macOS (/tmp -> /private/tmp), under /var, or a symlinked
// home/volume the main checkout and a worktree would get divergent scope strings
// and silently fail to pair. EvalSymlinks here yields ONE canonical form for
// every scope consumer (register's stored Scope, peers' scopeFilter, bootstrap's
// discovery all flow through resolveScope, the sole FindProjectRoot caller).
// Non-fatal: if the path can't be resolved (e.g. it does not exist yet) keep the
// lexical scope — never drop it. ProjectPath and the cwd lookup
// (LongestPrefixLookup) stay lexical on their own independent axis; Scope and
// ProjectPath are never compared to each other, so canonicalizing Scope is safe.
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
	if resolved, rerr := filepath.EvalSymlinks(scope); rerr == nil {
		return resolved
	}
	return scope
}

// evaluateResolution is the PURE B-1 guardrail predicate: given a cwd Resolution
// it decides what an id-free command should do, with NO I/O. It returns either
// an error (a HARD ambiguity always; a shared-scope hazard only under strict
// mode) or the selected id plus an optional warning string to print on stderr
// (a shared-scope hazard in the default mode). Kept separate from
// resolveCurrentSession so the policy is table-testable without os.Getwd/stderr.
//
// Every message names the remediation `--session-id=<id>` with the flag BEFORE
// any positional, consistent with A-1/A-5 — in a shared scope the caller must be
// able to copy-paste an EXECUTABLE command.
func evaluateResolution(cmdName, cwd string, res session.Resolution, strict bool) (sid, warnMsg string, err error) {
	if res.HardAmbiguous {
		return "", "", fmt.Errorf("%s: ambiguous: %d sessions match this cwd %q at the same path depth — pass one of: %s",
			cmdName, len(res.Candidates), cwd, formatCandidateChoices(res.Candidates))
	}
	if len(res.ScopeSiblings) > 0 {
		msg := formatSharedScopeWarning(cmdName, cwd, res)
		if strict {
			// Opt-in CAB_BRIDGE_STRICT_SESSION_LOOKUP=1 promotes the hazard to a
			// hard error (same text, no "warning:" prefix nuance needed — it is
			// returned as an error, which the cmd layer surfaces on stderr+exit 1).
			return "", "", errors.New(msg)
		}
		return res.SelectedID, msg, nil
	}
	return res.SelectedID, "", nil
}

// formatCandidateChoices renders the hard-ambiguity contenders as a list of
// executable `--session-id=<id> (<projectPath>)` choices, so the caller copies
// one verbatim. Flag-before-value, consistent with A-1/A-5.
func formatCandidateChoices(cands []session.Candidate) string {
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		parts = append(parts, fmt.Sprintf("--session-id=%s (%s)", c.ID, c.ProjectPath))
	}
	return strings.Join(parts, ", ")
}

// formatSharedScopeWarning renders the shared-scope hazard message: the resolved
// session, the sibling sessions sharing its scope with a different project path,
// and the executable remediation. Multi-line, on stderr only (the cmd layer
// keeps stdout clean for --json / --emit). The remediation names the resolved
// id with the flag before any positional (A-1/A-5).
func formatSharedScopeWarning(cmdName, cwd string, res session.Resolution) string {
	sel := res.Candidates[0] // == the SelectedID candidate
	var b strings.Builder
	fmt.Fprintf(&b, "%s: warning: cwd %q resolved to session %s (%s, role %s, project %s), but %d other session(s) share its scope %q with a different project path:",
		cmdName, cwd, sel.ID, sel.AgentName, sel.Role, sel.ProjectPath, len(res.ScopeSiblings), sel.Scope)
	for _, s := range res.ScopeSiblings {
		fmt.Fprintf(&b, "\n  - %s (%s, role %s, project %s)", s.ID, s.AgentName, s.Role, s.ProjectPath)
	}
	fmt.Fprintf(&b, "\n  pass --session-id=<id> to be explicit (e.g. cab-bridge %s --session-id=%s ...)", cmdName, sel.ID)
	return b.String()
}

// resolveCurrentSession resolves the session id an id-free command operates on,
// applying the B-1 scope-collision guardrail. It is the SINGLE chokepoint every
// id-free command routes through, so the policy lives in one place.
//
//   - An explicit --session-id BYPASSES the guardrail (resolveSessionID
//     validates and returns it). A disciplined caller that always passes the id
//     sees zero warnings — the warning appears only when the id is omitted in a
//     scope where it is genuinely ambiguous.
//   - Otherwise "me" is resolved from the cwd via LookupByCWDDetails and the pure
//     evaluateResolution predicate is applied: a HARD ambiguity is rejected; a
//     shared-scope hazard warns on stderr (or, with
//     CAB_BRIDGE_STRICT_SESSION_LOOKUP=1, is rejected). The warning is stderr
//     only — stdout stays clean for --json / --emit consumers.
func resolveCurrentSession(mgr *session.Manager, cmdName, sessionIDFlag string) (string, error) {
	if sessionIDFlag != "" {
		// Explicit id: bypass the guardrail (no lookup, no warning). Wrap the
		// validation error with cmdName so the bypass path is as well-labelled as
		// the lookup path below.
		sid, err := resolveSessionID(mgr, sessionIDFlag)
		if err != nil {
			return "", fmt.Errorf("%s: %w", cmdName, err)
		}
		return sid, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("%s: getwd for session lookup: %w", cmdName, err)
	}
	res, err := mgr.LookupByCWDDetails(cwd)
	if err != nil {
		if errors.Is(err, session.ErrNoSessionForCwd) {
			return "", fmt.Errorf("%s: no session for cwd %q — register first (or `cab-bridge bootstrap`), or pass --session-id=<id>", cmdName, cwd)
		}
		return "", fmt.Errorf("%s: session lookup from cwd %q: %w", cmdName, cwd, err)
	}
	sid, warnMsg, perr := evaluateResolution(cmdName, cwd, res, strictSessionLookup())
	if perr != nil {
		return "", perr
	}
	if warnMsg != "" {
		fmt.Fprintln(os.Stderr, warnMsg)
	}
	// Defensive SC-4 re-validation, consistent with resolveSessionID.
	if err := security.ValidateSessionID(sid); err != nil {
		return "", fmt.Errorf("%s: session lookup returned invalid id %q: %w", cmdName, sid, err)
	}
	return sid, nil
}

// strictSessionLookup reports whether the opt-in env var
// CAB_BRIDGE_STRICT_SESSION_LOOKUP promotes a shared-scope WARNING to a hard
// error (B-1). Default OFF — the hazard is a warning, never blocks (F-41/F-42
// non-regression). Read here, not in config, because it is a per-invocation
// safety toggle, not a tunable runtime parameter.
func strictSessionLookup() bool {
	return os.Getenv("CAB_BRIDGE_STRICT_SESSION_LOOKUP") == "1"
}
