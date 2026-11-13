package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-172 on the surface of `next`: what is said about a file that will not be delivered.

// plantDenied plants a valid message and takes the permission to read it away.
func plantDenied(t *testing.T, dataDir, sid, id string) {
	t.Helper()
	plantInboxAt(t, dataDir, sid, id, "valxxx01", message.TypeQuery, "denied", time.Now().UTC())
	denyReading(t, filepath.Join(dataDir, "sessions", sid, "inbox", id+".json"))
}

// The probe of the BAR, fixed as a test (criterion 1): on main it is red on three
// assertions, with the premise (the file really is denied) green.
func TestNext_AnUnreadableFileIsNotCorruptAndTheTextSaysItWillNotArrive(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "good", time.Now().UTC())
	plantDenied(t, dataDir, sid, "msg-bbbbbbbbbbbb")

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)

	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, messageIDs(p), "the readable message still gets through")
	assert.Equal(t, 0, p.CorruptCount, "a denied file is not a corrupt one")
	assert.Empty(t, p.Corrupt)
	assert.Equal(t, []unreadableFile{{File: "msg-bbbbbbbbbbbb.json", Cause: "permission denied"}}, p.Unreadable)
	all := strings.Join(p.Warnings, "\n")
	assert.Contains(t, all, `"msg-bbbbbbbbbbbb.json": permission denied`, "the file and the cause the system gave")
	assert.Contains(t, all, "NOT delivered")
	assert.Contains(t, all, "until then those messages will not arrive")
	assert.NotContains(t, all, "no action needed", "a message that will not arrive does need somebody")
	assert.NotContains(t, all, dataDir, "no path in the text")
	assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "inbox", "msg-bbbbbbbbbbbb.json"), "next moves nothing")
}

// A TEST OF THE TEXT, not of the product: the hint promises that `next` returns at
// once, and this test keeps that true for as long as the sentence is there. The day
// `next` waits on an inbox like this one (the finding that is open for it, with the
// shape `hasNew` already has in the MCP server), the sentence and this test change
// TOGETHER; if only the behaviour changes this test would defend the defect.
func TestNext_AnInboxWithOnlyUnreadableFilesSaysNothingCanBeDeliveredAndReturnsAtOnce(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantDenied(t, dataDir, sid, "msg-bbbbbbbbbbbb")

	for call := 1; call <= 2; call++ {
		// A wait of 5s: an `interrupted` here would mean next slept out the window.
		p := runNextOnce(t, mgr, cfg, sid, 5*time.Second)

		assert.Equal(t, nextStatusEmitted, p.Status, "call %d: reported, not slept out", call)
		assert.Equal(t, 0, p.Total)
		assert.Empty(t, p.Messages)
		assert.Len(t, p.Unreadable, 1, "call %d", call)
		assert.Contains(t, p.Hint, "nothing can be delivered now")
		assert.Contains(t, p.Hint, "returns at once and does not wait", "the promise this test keeps")
		assert.NotContains(t, p.Hint, "nothing readable arrived", "the old hint")
	}

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.Empty(t, cursor.Notified, "nothing was emitted, so nothing may be marked")
}

func TestNext_ACorruptFileIsNoLongerSaidToNeedNoAction(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "good", time.Now().UTC())
	inboxDir := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.WriteFile(filepath.Join(inboxDir, "msg-broken0000.json"), []byte("{not json"), 0o600))

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)

	assert.Equal(t, 1, p.CorruptCount, "unchanged: what is corrupt is still counted")
	assert.Equal(t, []string{"msg-broken0000.json"}, p.Corrupt)
	assert.Empty(t, p.Unreadable, "read and not decodable is corrupt, not unreadable")
	all := strings.Join(p.Warnings, "\n")
	assert.Contains(t, all, `"msg-broken0000.json"`)
	assert.Contains(t, all, "are not valid messages")
	assert.Contains(t, all, "NOT delivered")
	assert.Contains(t, all, "Move the file(s) out of the inbox and ask the sender to send again.")
	assert.NotContains(t, all, "no action needed")
}

func TestNext_AnInboxOfOnlyCorruptFilesGetsTheSameHintAndNoAction(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	inboxDir := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.MkdirAll(inboxDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(inboxDir, "msg-broken0000.json"), []byte("{nope"), 0o600))

	p := runNextOnce(t, mgr, cfg, sid, 5*time.Second)

	assert.Equal(t, nextStatusEmitted, p.Status)
	assert.Contains(t, p.Hint, "nothing can be delivered now")
	assert.NotContains(t, strings.Join(p.Warnings, "\n"), "no action needed")
}

func TestNext_TheTextListsAtMostFiveUnreadableNamesAndTheJSONListsThemAll(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "good", time.Now().UTC())
	for i := 1; i <= 7; i++ {
		plantDenied(t, dataDir, sid, fmt.Sprintf("msg-%012d", i))
	}

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)

	assert.Len(t, p.Unreadable, 7, "the JSON lists every one")
	all := strings.Join(p.Warnings, "\n")
	assert.Contains(t, all, "7 file(s)")
	assert.Contains(t, all, `"msg-000000000005.json": permission denied`, "the fifth is named")
	assert.NotContains(t, all, "msg-000000000006.json", "the sixth is counted, not named")
	assert.Contains(t, all, "and 2 more")
}

// Criterion 5: the contract grows and nothing is removed or renamed. The keys of an
// ordinary page are fixed here, so a new key on a normal inbox (or a lost one) fails.
func TestNext_TheJSONContractGrowsAndDoesNotChange(t *testing.T) {
	// The first record of a run, decoded to a map: a struct would hide a key that is absent.
	record := func(t *testing.T, mgr *session.Manager, cfg config.Config, sid string) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var stdout, stderr bytes.Buffer
		require.NoError(t, nextRun(ctx, mgr, cfg, sid, &stdout, &stderr))
		var page map[string]any
		require.NoError(t, json.NewDecoder(bytes.NewReader(stdout.Bytes())).Decode(&page), "the first record is the page: %s", stdout.String())
		return page
	}
	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}

	t.Run("an ordinary page has exactly the keys it had, and no new one", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "good", time.Now().UTC())
		page := record(t, mgr, cfg, sid)
		assert.Equal(t, []string{"agentName", "corruptCount", "generation", "hasMore", "hint", "messages", "openAsks", "returned", "session", "status", "total"}, keys(page))
	})

	t.Run("with unreadable and corrupt files every old key keeps its type and one is added", func(t *testing.T) {
		mgr, cfg, sid, dataDir := newNextSession(t)
		plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "good", time.Now().UTC())
		plantDenied(t, dataDir, sid, "msg-bbbbbbbbbbbb")
		require.NoError(t, os.WriteFile(filepath.Join(dataDir, "sessions", sid, "inbox", "msg-broken0000.json"), []byte("{nope"), 0o600))
		page := record(t, mgr, cfg, sid)

		assert.IsType(t, "", page["status"])
		assert.IsType(t, float64(0), page["corruptCount"])
		assert.Equal(t, float64(1), page["corruptCount"], "only the corrupt one is counted now")
		assert.Equal(t, []any{"msg-broken0000.json"}, page["corrupt"], "still an array of names")
		assert.IsType(t, []any{}, page["messages"])
		assert.IsType(t, []any{}, page["warnings"])
		assert.IsType(t, "", page["hint"])
		unreadable, ok := page["unreadable"].([]any)
		require.True(t, ok, "the new key is an array: %v", page["unreadable"])
		require.Len(t, unreadable, 1)
		assert.Equal(t, map[string]any{"file": "msg-bbbbbbbbbbbb.json", "cause": "permission denied"}, unreadable[0])
	})
}
