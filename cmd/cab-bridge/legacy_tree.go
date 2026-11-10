package main

// legacy_tree.go is the ONLY place that names the tree of the binaries that came
// before F-160 (<base>/sessions). This binary keeps its state in <base>/layout-2
// and does not use the old tree; what it knows about it is limited to two
// messages that tell a person why an id or a name does not resolve:
//
//   - a CAB_SESSION_ID that exists only in the old tree (the agent exported it
//     before the update) gets an error that says so and says what to do;
//   - an unknown recipient gets one more line saying how many sessions of the old
//     tree are not reachable from this version.
//
// The probes are read-only and never follow a link: the old tree's root is
// checked with Lstat first, and anything that is not a real directory (a link,
// a dangling link, a file, an error) means no hint at all. A link planted at
// <base>/sessions must not make this binary open its target, and a hint that
// disclosed the size of a directory elsewhere would be a leak (SEC gate round 3).
//
// DELETABLE: when no binary of the previous generation is supported any more,
// delete this file and its two call sites (resolveEnvSessionID, and the
// "no agent named" errors in resolveRecipientByName).

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
)

// legacyCountCap bounds how many entries of the old tree are counted: the number
// is a hint, and an unbounded ReadDir over a directory somebody filled is not.
const legacyCountCap = 1000

// legacySessionsRoot returns <base>/sessions and true only when that path is a
// REAL directory. Lstat does not follow the last component, so a symlink — even
// one pointing at a directory — is refused, as is a file, a dangling link and
// any error: for a hint, "cannot tell" and "not there" are the same answer.
func legacySessionsRoot(cfg config.Config) (string, bool) {
	root := filepath.Join(cfg.BaseDir, "sessions")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return root, true
}

// legacySessionHint is the sentence appended to the error for a CAB_SESSION_ID
// that does not exist in the current tree. Empty unless the id names an entry of
// the old tree. sid must already be a valid session id (the caller validated it
// before looking it up), so it cannot carry a path separator.
func legacySessionHint(cfg config.Config, sid string) string {
	root, ok := legacySessionsRoot(cfg)
	if !ok {
		return ""
	}
	if _, err := os.Lstat(filepath.Join(root, sid)); err != nil {
		return ""
	}
	return fmt.Sprintf(" — this id belongs to the tree of a cab-bridge from before the update (%s), which this version does not use: run `cab-bridge join` again to get a new id", root)
}

// legacyRecipientHint is the sentence appended to a "no agent named" error:
// how many sessions of the old tree exist and cannot be reached from here.
// Empty when the old tree is absent, unreadable, a link, or holds no entries.
// Only entry NAMES are counted; no manifest or message of the old tree is read.
func legacyRecipientHint(cfg config.Config) string {
	root, ok := legacySessionsRoot(cfg)
	if !ok {
		return ""
	}
	dir, err := os.Open(root)
	if err != nil {
		return ""
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(legacyCountCap + 1)
	if err != nil || len(entries) == 0 {
		return ""
	}
	count := fmt.Sprintf("%d", len(entries))
	if len(entries) > legacyCountCap {
		count = fmt.Sprintf("more than %d", legacyCountCap)
	}
	return fmt.Sprintf(" — %s session(s) of the tree of a cab-bridge from before the update exist in %s and are not reachable from this version", count, root)
}
