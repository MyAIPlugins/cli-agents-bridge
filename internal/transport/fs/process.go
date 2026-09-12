package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MoveToProcessed renames a message file from inbox/ into a sibling
// processed/ directory with a timestamped name. Implements the inbox policy
// "A→B migration" from Sprint 3 brief: PollInbox no longer deletes consumed
// messages but moves them to processed/ preserving audit-trail order.
//
// Naming convention: processed/<RFC3339-timestamp>-<original-basename>.
// The timestamp prefix gives lexical sort = chronological sort under `ls`,
// which the Sprint 4 transcript feature (PLAN §5 v0.3+) will consume
// directly without re-sorting.
//
// Atomicity: rename(2) is atomic when src and dst are on the same
// filesystem (POSIX). processedDir is computed as a sibling of inbox/ so
// they always share the filesystem in any realistic config — but it ARRIVES AS
// A PARAMETER, so unlike AtomicWriteBytes this branch is genuinely reachable:
// a caller can name a directory on another volume. A cross-device move is
// surfaced as an explicit error — no silent copy-fallback
// (docs/dev-conventions.md "No implicit fallbacks").
//
// The condition is recognised by isCrossDevice, which is PER-OS and had to
// become so: this line used to say EXDEV and the code used to test for it,
// which on Windows is a constant the system never returns (F-137). The branch
// was dead there while this comment announced it was covered.
//
// Creates processedDir with mode 0o700 (SC-2) if it does not yet exist.
func MoveToProcessed(srcInboxPath, processedDir string) error {
	if err := os.MkdirAll(processedDir, 0o700); err != nil {
		return fmt.Errorf("mkdir processed %q: %w", processedDir, err)
	}

	base := filepath.Base(srcInboxPath)
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	dstName := stamp + "-" + base
	dstPath := filepath.Join(processedDir, dstName)

	if err := renameAtomic(srcInboxPath, dstPath); err != nil {
		if isCrossDevice(err) {
			return fmt.Errorf("move %q -> %q: cross-device rename is not atomic — inbox and processed dirs must share filesystem (config bug, not transient): %w",
				srcInboxPath, dstPath, err)
		}
		return fmt.Errorf("move %q -> %q: %w", srcInboxPath, dstPath, err)
	}
	return nil
}
