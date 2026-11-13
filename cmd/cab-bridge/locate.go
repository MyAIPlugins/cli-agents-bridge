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
// the delivery of a response all ask this question, so it lives in a file of its own. They do not
// ask it the same way: locateMessage finds one message, locateCopiesStrict every copy of it.

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
// the defect F-178 closes). The readers (read, ack, reply of an ask) take the first by name, which
// starts with the instant of the archiving, so the oldest: they want one message and that is the
// choice they always had. The delivery of a response cannot choose one: see locateCopiesStrict.
//
// An entry of processed/ that cannot be verified as ours is skipped here, whatever the reason.
// That is right for the readers. It is not for a caller that ACTS on "not found": see
// locateCopiesStrict.
func locateMessage(sessionDir, id string) (located, error) {
	found, err := find(sessionDir, id, false, false)
	if err != nil {
		return located{}, err
	}
	if len(found) == 0 {
		return located{}, errMessageNotFound
	}
	return found[0], nil
}

// locateCopiesStrict is the look of the delivery of a response (F-178), where "nothing found" ends
// in a message being written. It differs from locateMessage in two ways, both for the same reason:
// the answer must not be able to hide a message that is there.
//
// It returns EVERY copy under the id, not the first: the inbox/ one if there is one, then those of
// processed/ by name. Judging the one that sorts first would let a copy with other bytes pass
// whenever an identical one has the earlier name, and refuse the same two files with the names
// swapped. The caller looks at all of them and an empty result means there is none.
//
// An entry of processed/ that is not ours (another owner, a symlink, not a regular file:
// security.ErrOwnershipMismatch) is still skipped, because a name alone must not forge "archived".
// An entry whose check fails for any other reason (no permission to open it, gone between the
// listing and the check) is an ERROR, and so is a processed/ that cannot be listed: skipping it
// could be the copy that was delivered, and an error is the one answer that cannot write a second
// one. A retry looks again.
func locateCopiesStrict(sessionDir, id string) ([]located, error) {
	return find(sessionDir, id, true, true)
}

// find collects the copies of a message: the one in inbox/, then those of processed/ in the order
// of their names. With all=false it stops at the first it finds, which is what the readers want
// and spares them the listing of processed/ when the message is in inbox/.
func find(sessionDir, id string, strict, all bool) ([]located, error) {
	if err := message.ValidateMessageID(id); err != nil {
		return nil, err
	}
	var found []located
	inboxPath := filepath.Join(sessionDir, "inbox", id+".json")
	if _, err := os.Lstat(inboxPath); err == nil {
		found = append(found, located{path: inboxPath})
		if !all {
			return found, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("look for %s in the inbox: %w", id, err)
	}
	processedDir := filepath.Join(sessionDir, "processed")
	entries, err := os.ReadDir(processedDir)
	if err != nil {
		if os.IsNotExist(err) {
			return found, nil
		}
		return nil, fmt.Errorf("look for %s in processed/: %w", id, err)
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
				return nil, fmt.Errorf("check %s in processed/: %w", e.Name(), cerr)
			}
			continue
		}
		found = append(found, located{path: path, archived: true})
		if !all {
			return found, nil
		}
	}
	return found, nil
}
