// Package fs provides atomic write helpers for cli-agents-bridge filesystem
// IPC. Backs Security Control SC-5 (atomic write, perms 600) and FIX-7
// (atomic write semantics — temp same-fs + fsync + rename, explicit EXDEV
// fail on cross-filesystem).
//
// Atomicity guarantee: rename(2) is atomic on POSIX when source and target
// live on the same filesystem. We enforce same-fs by creating the temp file
// in the target directory (os.CreateTemp(filepath.Dir(target), ...)).
// A cross-device rename is surfaced as an explicit error rather than a silent
// non-atomic fallback (docs/dev-conventions.md "No implicit fallbacks"), and
// the condition is recognised by isCrossDevice, which is PER-OS.
//
// It has to be: the sentence above used to say EXDEV and the code used to test
// for it, which on Windows is a constant the system never returns there
// (F-137). The branch was dead and the comment said it was alive — the second
// half being the part that costs somebody an afternoon.
//
// Durability: f.Sync() flushes data + minimal metadata before rename, so a
// kernel crash mid-write cannot leave a zero-byte file (Linux ext4 historic
// bug — see https://www.evanjones.ca/durability-filesystem.html). On macOS
// APFS copy-on-write the sync is largely redundant but harmless.
package fs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// AtomicWriteJSON marshals v as indented JSON and writes to path atomically
// with mode 0o600.
func AtomicWriteJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json for %q: %w", path, err)
	}
	return AtomicWriteBytes(path, data, 0o600)
}

// AtomicWriteBytes writes data to path atomically using temp file + fsync +
// rename, ending with the requested mode. Same-filesystem guarantee: the
// temp file is created in filepath.Dir(path).
//
// The cross-device branch is DEFENCE IN DEPTH, and it says so rather than
// implying more: the temp file is created in filepath.Dir(path), so source and
// target are in the SAME DIRECTORY and therefore on the same volume by
// construction. There is no configuration that reaches it from here. It stays
// because the guarantee it states is the one the whole function rests on, and
// because a rename that ever did cross devices must fail loudly rather than be
// mistaken for a transient error — but nobody should go looking for the case
// that triggers it, because there is not one.
//
// MoveToProcessed is the call-site where the same branch IS reachable: its
// destination comes from the caller.
func AtomicWriteBytes(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)

	f, err := os.CreateTemp(dir, ".tmp.*")
	if err != nil {
		return fmt.Errorf("createtemp in %q: %w", dir, err)
	}
	tmpPath := f.Name()

	// Cleanup defer: remove tmp on any error path. Cleared at end on success.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write to tmp %q: %w", tmpPath, err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync tmp %q: %w", tmpPath, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp %q: %w", tmpPath, err)
	}

	// Explicit chmod for clarity — umask 077 already produces 0o600 for the
	// default os.CreateTemp call, but being explicit removes hidden coupling
	// with main.go init() and protects test runs that override umask.
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod tmp %q to %o: %w", tmpPath, mode, err)
	}

	if err := renameAtomic(tmpPath, path); err != nil {
		if isCrossDevice(err) {
			return fmt.Errorf("rename %q -> %q: cross-device rename is not atomic — temp dir and target must share filesystem (this is a config bug, not a transient failure): %w", tmpPath, path, err)
		}
		return fmt.Errorf("rename %q -> %q: %w", tmpPath, path, err)
	}

	tmpPath = "" // disable cleanup defer (rename consumed the temp file)
	return nil
}

// ReadJSON reads path and unmarshals into v, refusing files owned by another
// uid (SC-3, security.ReadOwnedFile).
//
// The check lives HERE, not in each caller, because "the caller is responsible"
// was the arrangement that left CheckOwnership with zero production call-sites
// for seven releases: a duty spread across every read site is a duty nobody
// performs. One reader, one rule.
func ReadJSON(path string, v interface{}) error {
	data, err := security.ReadOwnedFile(path)
	if err != nil {
		return fmt.Errorf("read %q: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshal %q: %w", path, err)
	}
	return nil
}

// WriteIfAbsentBytes creates path with data only if path does not exist yet,
// and reports whether it did the creating.
//
// If path already holds byte-identical content the call succeeds with
// created=false: that is the "already delivered" case of an idempotent retry
// (DESIGN v0.8 §2.3). Different content under the same name is an ERROR — two
// distinct messages collided on one id, which must never pass silently.
//
// Create-if-absent is done with link(2) rather than a stat-then-write: link
// fails with EEXIST atomically, so two concurrent retries cannot both believe
// they created the file. The payload is written to a temp file in the same
// directory first, so the linked file is never partially visible.
func WriteIfAbsentBytes(path string, data []byte, mode os.FileMode) (created bool, err error) {
	dir := filepath.Dir(path)

	f, err := os.CreateTemp(dir, ".tmp.*")
	if err != nil {
		return false, fmt.Errorf("create temp for %q: %w", path, err)
	}
	tmpPath := f.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return false, fmt.Errorf("write temp for %q: %w", path, werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return false, fmt.Errorf("sync temp for %q: %w", path, serr)
	}
	if cerr := f.Close(); cerr != nil {
		return false, fmt.Errorf("close temp for %q: %w", path, cerr)
	}
	if cerr := os.Chmod(tmpPath, mode); cerr != nil {
		return false, fmt.Errorf("chmod temp for %q: %w", path, cerr)
	}

	if lerr := os.Link(tmpPath, path); lerr == nil {
		return true, nil
	} else if !errors.Is(lerr, fs.ErrExist) {
		return false, fmt.Errorf("link temp to %q: %w", path, lerr)
	}

	existing, rerr := security.ReadOwnedFile(path)
	if rerr != nil {
		return false, fmt.Errorf("read existing %q: %w", path, rerr)
	}
	if !bytes.Equal(existing, data) {
		return false, fmt.Errorf("%q already exists with different content — refusing to overwrite", path)
	}
	return false, nil
}
