// Package fs provides atomic write helpers for cli-agents-bridge filesystem
// IPC. Backs Security Control SC-5 (atomic write, perms 600) and FIX-7
// (atomic write semantics — temp same-fs + fsync + rename, explicit EXDEV
// fail on cross-filesystem).
//
// Atomicity guarantee: rename(2) is atomic on POSIX when source and target
// live on the same filesystem. We enforce same-fs by creating the temp file
// in the target directory (os.CreateTemp(filepath.Dir(target), ...)).
// Cross-filesystem rename returns EXDEV explicitly — we surface it as an
// error rather than silent non-atomic fallback (no fallback impliciti per
// CLAUDE.md).
//
// Durability: f.Sync() flushes data + minimal metadata before rename, so a
// kernel crash mid-write cannot leave a zero-byte file (Linux ext4 historic
// bug — see https://www.evanjones.ca/durability-filesystem.html). On macOS
// APFS copy-on-write the sync is largely redundant but harmless.
package fs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
// Returns a wrapped EXDEV error if rename crosses filesystems — this would
// indicate a misconfigured data dir (target on different mount than parent
// dir we resolved), not a runtime issue. No silent copy-fallback (no
// fallback impliciti).
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

	if err := os.Rename(tmpPath, path); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("rename %q -> %q: EXDEV cross-filesystem rename is not atomic — temp dir and target must share filesystem (this is a config bug, not a transient failure): %w", tmpPath, path, err)
		}
		return fmt.Errorf("rename %q -> %q: %w", tmpPath, path, err)
	}

	tmpPath = "" // disable cleanup defer (rename consumed the temp file)
	return nil
}

// ReadJSON reads path and unmarshals into v. Caller is responsible for
// ownership validation via security.CheckOwnership(path) before invoking
// ReadJSON (SC-3 layered defense — this helper does not enforce it).
func ReadJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %q: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshal %q: %w", path, err)
	}
	return nil
}
