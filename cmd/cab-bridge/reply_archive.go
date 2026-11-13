package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// archiveCloseIDs archives exactly and only the frozen closeIDs of the reply, resuming from the
// index. By id, never by re-scanning the directory: a message that arrived after the snapshot
// must not be swept away unseen (§2.3 invariant).
//
// Each ask is one step, and each step runs under the lock of the responder's session with the
// journal checked first (WithReplyTxn, F-177): the move into processed/ and the record of the index
// are decided on a journal that is still this reply's, in a session that still exists, and nobody
// who takes that lock (a tidy, an ack, a retirement, a delivery to this session) can be between
// the two. A driver that is behind therefore moves nothing: before F-177 it moved the ask first and
// found out afterwards, and on a retired session it recreated processed/ in a folder that was gone.
func archiveCloseIDs(mgr *session.Manager, cfg config.Config, sid string, txn *session.ReplyTxn, move func(src, processedDir string) error) error {
	processedDir := filepath.Join(cfg.DataDir, "sessions", sid, "processed")
	inboxDir := filepath.Join(cfg.DataDir, "sessions", sid, "inbox")
	for txn.ArchivedIndex < len(txn.CloseIDs) {
		id := txn.CloseIDs[txn.ArchivedIndex]
		src := filepath.Join(inboxDir, id+".json")
		err := mgr.WithReplyTxn(sid, txn, func() error {
			// An ask that is already out of the inbox is the end state this step wants (a crash
			// between the move and the write of the index leaves exactly that, and so does an ask
			// closed by ack or tidy). MoveToProcessed wraps what it gets with %w, so the test has to
			// be errors.Is: os.IsNotExist does not look inside a wrapped error, and with it the
			// journal could never complete (F-176).
			if merr := move(src, processedDir); merr != nil && !errors.Is(merr, fs.ErrNotExist) {
				return fmt.Errorf("archive %s: %w", id, merr)
			}
			txn.ArchivedIndex++
			// The lock is re-entrant inside the process, so this is the same critical section; it
			// checks the journal once more, which is the cost of going through the one operation
			// that is allowed to write it.
			return mgr.UpdateReplyTxn(sid, txn)
		})
		if err != nil {
			return fmt.Errorf("reply: %w", err)
		}
	}
	return nil
}
