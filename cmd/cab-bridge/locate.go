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
func locateMessage(sessionDir, id string) (located, error) {
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
			continue
		}
		return located{path: path, archived: true}, nil
	}
	return located{}, errMessageNotFound
}
