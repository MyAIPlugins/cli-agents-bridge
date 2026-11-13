package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// F-172. A file the system will not let us read is not a corrupt file, and a file that
// is gone between the listing and the read is neither.

// denyReading takes every permission off a planted file so that the system refuses a
// read, and ASSERTS the premise: a test of "the file cannot be read" that cannot make a
// file unreadable would pass on one that reads fine (LL-12). chmod 000 denies nothing on
// Windows and root reads anything, so the tests that need it skip there.
func denyReading(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX file permissions and a non-root user: chmod 000 does not deny a read on Windows, and root reads anything")
	}
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Logf("restore permissions of %s: %v", path, err)
		}
	})
	_, err := os.ReadFile(path)
	require.ErrorIs(t, err, fs.ErrPermission, "the premise of the test: the system must refuse this read")
}

func entryIDs(entries []mailboxEntry) []string {
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.msg.ID)
	}
	return ids
}

func TestReadMailbox_ADeniedFileIsUnreadableWithTheSystemsCause(t *testing.T) {
	_, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "fine", time.Now().UTC())
	plantInboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "valxxx01", message.TypeQuery, "denied", time.Now().UTC())
	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	denyReading(t, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"))

	scan, err := readMailbox(inbox, cfg.MaxMessageBytes)
	require.NoError(t, err)

	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, entryIDs(scan.entries), "the readable message is still read")
	assert.Empty(t, scan.corrupt, "a file the system will not let us read is not a corrupt file")
	assert.Empty(t, scan.foreign)
	require.Len(t, scan.unreadable, 1)
	assert.Equal(t, "msg-bbbbbbbbbbbb.json", scan.unreadable[0].File)
	assert.Equal(t, "permission denied", scan.unreadable[0].Cause, "what the system said, in its shortest form")
	assert.NotContains(t, scan.unreadable[0].Cause, inbox, "the cause never carries the path")
	assert.NotContains(t, scan.unreadable[0].File, string(filepath.Separator), "a name, not a path")
}

// The file is removed INSIDE the read, so the listing already saw it and the read does
// not: what a `reply` or a `tidy` does between the two. The ENOENT is the one the real
// read gives, not an error built by the test, and the test asserts that it got it.
func TestReadMailbox_AFileThatVanishesBetweenListingAndReadIsNeitherCorruptNorUnreadable(t *testing.T) {
	_, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "stays", time.Now().UTC())
	plantInboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "valxxx01", message.TypeQuery, "goes away", time.Now().UTC())
	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	vanishing := filepath.Join(inbox, "msg-bbbbbbbbbbbb.json")

	var seen error
	read := func(path string) ([]byte, error) {
		if path != vanishing {
			return security.ReadOwnedFile(path)
		}
		require.NoError(t, os.Remove(path))
		data, err := security.ReadOwnedFile(path)
		seen = err
		return data, err
	}

	scan, err := readMailboxWith(inbox, cfg.MaxMessageBytes, read)
	require.NoError(t, err)
	require.ErrorIs(t, seen, fs.ErrNotExist, "the premise: the real read of the vanished file said ENOENT")

	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, entryIDs(scan.entries))
	assert.Empty(t, scan.corrupt, "a file that is gone is not a corrupt file")
	assert.Empty(t, scan.unreadable, "a file that is gone is not one the system refuses")
	assert.Empty(t, scan.foreign)
}

// What the BAR leaves alone: a file that IS read and does not decode stays corrupt.
func TestReadMailbox_ACorruptFileStaysCorrupt(t *testing.T) {
	_, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "fine", time.Now().UTC())
	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	for name, body := range map[string]string{
		"msg-truncated0.json": "{not json",
		"msg-empty00000.json": "",
	} {
		path := filepath.Join(inbox, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		_, err := os.ReadFile(path)
		require.NoError(t, err, "the premise: %s IS readable, it just is not a message", name)
	}

	scan, err := readMailbox(inbox, cfg.MaxMessageBytes)
	require.NoError(t, err)

	assert.Equal(t, []string{"msg-empty00000.json", "msg-truncated0.json"}, scan.corrupt, "sorted, both declared")
	assert.Empty(t, scan.unreadable, "read and not decodable is corrupt, not unreadable")
	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, entryIDs(scan.entries))
}

// The branch next to the new one: a link is still a file that is not ours.
func TestReadMailbox_ALinkIsStillForeignAndNotUnreadable(t *testing.T) {
	_, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "fine", time.Now().UTC())
	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	target := filepath.Join(dataDir, "elsewhere.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"id":"msg-bbbbbbbbbbbb"}`), 0o600))
	mustSymlink(t, target, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"))

	scan, err := readMailbox(inbox, cfg.MaxMessageBytes)
	require.NoError(t, err)

	assert.Equal(t, []string{"msg-bbbbbbbbbbbb.json"}, scan.foreign)
	assert.Empty(t, scan.unreadable)
	assert.Empty(t, scan.corrupt)
}

func TestCauseOf_IsTheLeafOfTheChainWithoutThePath(t *testing.T) {
	t.Parallel()
	const path = "/data/cab/sessions/abc12345/inbox/msg-x.json"
	denied := &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"a path error is its cause", denied, "permission denied"},
		{"wrapped once, with the path in the wrapper", fmt.Errorf("stat %q: %w", path, denied), "permission denied"},
		{"wrapped by the retry of F-154, which the cause drops on purpose",
			fmt.Errorf("%w (still refused after 6 attempts, 155ms of backoff requested)", denied), "permission denied"},
		{"an error with no chain is its own text", errors.New("disk on fire"), "disk on fire"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := causeOf(c.err)
			assert.Equal(t, c.want, got)
			assert.NotContains(t, got, path, "the path stays in the wrapper")
		})
	}
}

type cyclicError struct{}

func (c cyclicError) Error() string { return "cycle" }
func (c cyclicError) Unwrap() error { return c }

// The bound is against a cycle of Unwrap, and it must not change the answer: at the
// bound the text found there is returned, which is still true, never a made-up constant.
func TestCauseOf_AChainLongerThanTheBoundEndsWithTheTextFoundThereAndACycleDoesNotHang(t *testing.T) {
	t.Parallel()
	nodes := []error{errors.New("leaf")}
	for i := 1; i <= maxCauseDepth+4; i++ {
		nodes = append(nodes, fmt.Errorf("w%d: %w", i, nodes[i-1]))
	}
	got := causeOf(nodes[len(nodes)-1])
	assert.Equal(t, nodes[len(nodes)-1-maxCauseDepth].Error(), got, "the text of the node the bound stopped at")
	assert.NotEqual(t, "leaf", got, "the premise: the bound really was reached")

	assert.Equal(t, "cycle", causeOf(cyclicError{}), "a cycle ends at the bound")
}
