package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// Finding a message of a session by its id. The MCP tools (read, ack, reply) and, since F-178,
// the delivery of a response all ask this question, so it lives in a file of its own.

// errMessageNotFound is returned by locateMessage for an id that is in neither the
// inbox nor processed/.
var errMessageNotFound = errors.New("no such message in the inbox or in processed/")

// located says where a message was found.
type located struct {
	path     string
	archived bool
}

// locateMessage finds a message by id WITHOUT decoding every file: in inbox/ the file
// is named <id>.json, in processed/ it is <timestamp>-<id>.json. The id is validated
// against the wire format before it is used to build any path.
//
// THE ORDER IS PART OF THE ANSWER: inbox/ first, processed/ second. A message only ever
// moves one way, from inbox/ to processed/, and the move is one rename, so it is always in
// one of the two. Whoever does not see it in inbox/ at one instant finds it in processed/ at
// a later one; the reverse order could see it in neither while another process moves it
// between the two looks, and "nowhere" is the answer that makes a delivery write a second copy.
//
// Two entries of processed/ can carry the same id (two copies of one response, an artifact of
// the defect F-178 closes): the first by name is returned, and the name starts with the instant
// of the archiving, so it is the oldest.
//
// An entry of processed/ that cannot be verified as ours is skipped here, whatever the reason.
// That is right for the readers (read, ack, reply of an ask). It is not for a caller that ACTS
// on "not found": see locateMessageStrict.
func locateMessage(sessionDir, id string) (located, error) {
	return locate(sessionDir, id, false)
}

// locateMessageStrict is locateMessage for the delivery of a response (F-178), where "not found"
// ends in a message being written. An entry of processed/ that is not ours (another owner, a
// symlink, not a regular file: security.ErrOwnershipMismatch) is still skipped, because a name
// alone must not forge "archived". An entry whose check fails for any other reason (no
// permission to open it, gone between the listing and the check) is an ERROR: skipping it could
// be the copy that was delivered, and an error is the one answer that cannot write a second one.
// A retry looks again.
func locateMessageStrict(sessionDir, id string) (located, error) {
	return locate(sessionDir, id, true)
}

func locate(sessionDir, id string, strict bool) (located, error) {
	if err := message.ValidateMessageID(id); err != nil {
		return located{}, err
	}
	inboxPath := filepath.Join(sessionDir, "inbox", id+".json")
	if _, err := os.Lstat(inboxPath); err == nil {
		return located{path: inboxPath}, nil
	} else if !os.IsNotExist(err) {
		return located{}, fmt.Errorf("look for %s in the inbox: %w", id, err)
	}
	processedDir := filepath.Join(sessionDir, "processed")
	entries, err := os.ReadDir(processedDir)
	if err != nil {
		if os.IsNotExist(err) {
			return located{}, errMessageNotFound
		}
		return located{}, fmt.Errorf("look for %s in processed/: %w", id, err)
	}
	suffix := "-" + id + ".json"
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		path := filepath.Join(processedDir, e.Name())
		// A name alone must not forge "archived": the entry has to be ours.
		if cerr := security.CheckOwnedFile(path); cerr != nil {
			if strict && !errors.Is(cerr, security.ErrOwnershipMismatch) {
				return located{}, fmt.Errorf("check %s in processed/: %w", e.Name(), cerr)
			}
			continue
		}
		return located{path: path, archived: true}, nil
	}
	return located{}, errMessageNotFound
}
