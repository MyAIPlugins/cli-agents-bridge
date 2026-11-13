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
func archiveCloseIDs(mgr *session.Manager, cfg config.Config, sid string, txn *session.ReplyTxn, move func(src, processedDir string) error) error {
	processedDir := filepath.Join(cfg.DataDir, "sessions", sid, "processed")
	inboxDir := filepath.Join(cfg.DataDir, "sessions", sid, "inbox")
	for txn.ArchivedIndex < len(txn.CloseIDs) {
		id := txn.CloseIDs[txn.ArchivedIndex]
		src := filepath.Join(inboxDir, id+".json")
		// An ask that is already out of the inbox is the end state this step wants (a crash
		// between the move and the write of the index leaves exactly that, and so does an ask
		// closed by ack or tidy). MoveToProcessed wraps what it gets with %w, so the test has to
		// be errors.Is: os.IsNotExist does not look inside a wrapped error, and with it the
		// journal could never complete (F-176).
		if err := move(src, processedDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("reply: archive %s: %w", id, err)
		}
		txn.ArchivedIndex++
		if err := mgr.UpdateReplyTxn(sid, txn); err != nil {
			return fmt.Errorf("reply: %w", err)
		}
	}
	return nil
}
