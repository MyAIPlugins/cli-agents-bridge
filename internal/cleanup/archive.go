package cleanup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// archiveStopped opens every error of the archive step. The sentence is true of all of them:
// the step runs BEFORE the manifest is removed, so when it fails the session is still a
// session, and nothing has been deleted (a message that was moved is in the archive, which
// is not the same thing).
const archiveStopped = "cleanup: session %q: archive stopped before the retirement, nothing was deleted: "

// archiveMove is one message file on its way from a folder of the session to the archive.
type archiveMove struct{ src, dst string }

// archiveCalls resolves the three seams to the calls production makes. The rename is the
// one of F-133, which on Windows retries a transient refusal by itself: this package has no
// retry and no wait of its own (structure_test.go fixes both).
func (s retireSeams) archiveCalls() (readDir func(string) ([]os.DirEntry, error), mkdirAll func(string, os.FileMode) error, rename func(string, string) error) {
	readDir, mkdirAll, rename = s.readDir, s.mkdirAll, s.rename
	if readDir == nil {
		readDir = os.ReadDir
	}
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if rename == nil {
		rename = transportfs.RenameAtomic
	}
	return readDir, mkdirAll, rename
}

// archiveMessages puts every message of the session into archive/<date>/<sid>/<folder>/,
// or stops, in three steps and in this order:
//
//  1. LOOK. List inbox/, outbox/ and processed/. A folder that does not exist has nothing to
//     archive; any other error listing it stops the step, and so does a directory inside a
//     message folder (no code of this project puts one there, it is not a message, and
//     archiving it would change what is archived).
//  2. PREPARE. Create the archive folders the moves need.
//  3. MOVE. Rename the files one by one; the first error stops the step.
//
// Steps 1 and 2 move nothing, so a failure there leaves the session exactly as it was. A
// failure in step 3 leaves a PREFIX moved: those messages are in the archive, the others are
// in the session, the manifest is still there, and the next cleanup pass finishes the job.
// That is why the error of step 3 says how many are already in the archive: of those, the
// unread ones are no longer shown by `next`, and `sent` may report them as expired (it finds
// the session but not the message) until the pass that completes.
//
// An empty archive folder can be left behind by a failure after step 2 (and, if the pass
// that completes runs on another day, an empty <sid> folder under the first date). It is
// harmless, but the retention sweep counts the folders under a date as archived sessions,
// so PurgedSessionCount can count one.
//
// Rollback was the alternative, and it is not here: it can fail too, and then leaves the
// same partial state with more code. Everything that is not a message (listener.json and
// the like) is still removed without an archive, as before.
func archiveMessages(sid, sessionDir, archiveSessionDir string, seams retireSeams) error {
	readDir, mkdirAll, rename := seams.archiveCalls()

	var moves []archiveMove
	var folders []string
	for _, sub := range messageSubdirs {
		archDir := filepath.Join(archiveSessionDir, sub)
		planned, err := planFolder(sid, filepath.Join(sessionDir, sub), archDir, readDir)
		if err != nil {
			return err
		}
		if len(planned) == 0 {
			continue // missing or empty folder: nothing to archive
		}
		moves = append(moves, planned...)
		folders = append(folders, archDir)
	}

	for _, dir := range folders {
		if err := mkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf(archiveStopped+"cannot create the archive folder %s (nothing was moved, the next cleanup pass tries again): %w", sid, dir, err)
		}
	}

	for i, m := range moves {
		if err := rename(m.src, m.dst); err != nil {
			return fmt.Errorf(archiveStopped+"cannot move %s into the archive (%s): %w", sid, m.src, movedProgress(i, len(moves)), err)
		}
	}
	return nil
}

// planFolder lists one message folder and names where each file goes.
func planFolder(sid, srcDir, archDir string, readDir func(string) ([]os.DirEntry, error)) ([]archiveMove, error) {
	entries, err := readDir(srcDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(archiveStopped+"cannot list %s (nothing was moved, the next cleanup pass tries again): %w", sid, srcDir, err)
	}
	moves := make([]archiveMove, 0, len(entries))
	for _, e := range entries {
		src := filepath.Join(srcDir, e.Name())
		if e.IsDir() {
			return nil, fmt.Errorf(archiveStopped+"%s is a directory inside a message folder and is not archived (nothing was moved): move it elsewhere or remove it by hand, then run the cleanup again", sid, src)
		}
		moves = append(moves, archiveMove{src: src, dst: filepath.Join(archDir, e.Name())})
	}
	return moves, nil
}

// movedProgress says what state a failed move left, and what to do about it.
func movedProgress(done, total int) string {
	if done == 0 {
		return "no message was moved yet, the next cleanup pass tries again"
	}
	return fmt.Sprintf("%d of %d messages are already in the archive, so next no longer shows the unread ones among them and sent may report them as expired: run the full cleanup again to finish", done, total)
}
