package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// recordingRunner is the injected hookRunner for watchTick tests: it records the
// env of every invocation and can be told to fail, so the tick logic is tested
// without spawning a real process.
type recordingRunner struct {
	calls     [][]string
	fail      bool // fail every call
	failFirst int  // fail the first N calls, then succeed (for retry tests)
}

func (r *recordingRunner) run(_ context.Context, env []string) error {
	r.calls = append(r.calls, env)
	if r.fail || len(r.calls) <= r.failFirst {
		return errors.New("hook boom")
	}
	return nil
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

// watchTestState creates the notify-watch/ dir (so state saves succeed) and
// returns the state path plus a fresh empty state.
func watchTestState(t *testing.T, sessionDir string) (string, *watchState) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, notifyWatchDir), 0o700))
	return filepath.Join(sessionDir, notifyWatchDir, "default.json"), &watchState{Entries: map[string]*watchEntry{}}
}

func watchTestCfg() watchConfig {
	return watchConfig{pollInterval: time.Second, hookTimeout: time.Second, hookArgv: []string{"true"}}
}

// Non-negotiable #2: notify-watch is a PEEK. Messages stay in inbox/ after a tick.
func TestWatchTick_PeekNonConsuming(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess01"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief")
	statePath, st := watchTestState(t, sessionDir)
	fr := &recordingRunner{}

	err := watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{})
	require.NoError(t, err)
	assert.Len(t, fr.calls, 1, "one pending message → one hook")
	assert.FileExists(t, filepath.Join(sessionDir, "inbox", "msg-aaaaaaaaaaaa.json"), "the message must stay in inbox (not consumed)")
}

// Non-negotiable #3: N pending → ONE hook, with the batch metadata in the env.
func TestWatchTick_CoalescesBatch(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess02"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "one")
	plantMsg(t, dataDir, sid, "inbox", "msg-bbbbbbbbbbbb", "val12345", "VAL-x", message.TypeResponse, "two")
	plantMsg(t, dataDir, sid, "inbox", "msg-cccccccccccc", "val12345", "VAL-x", message.TypeNotify, "three")
	statePath, st := watchTestState(t, sessionDir)
	fr := &recordingRunner{}

	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	require.Len(t, fr.calls, 1, "three pending → ONE coalesced hook, never 3")
	assert.Equal(t, "3", envValue(fr.calls[0], "CAB_MSG_COUNT"))
	assert.Equal(t, sid, envValue(fr.calls[0], "CAB_SESSION_ID"))
	ids := envValue(fr.calls[0], "CAB_MSG_IDS")
	assert.Contains(t, ids, "msg-aaaaaaaaaaaa")
	assert.Contains(t, ids, "msg-bbbbbbbbbbbb")
	assert.Contains(t, ids, "msg-cccccccccccc")
}

// Non-negotiable #4: once notified (hook exit-0), the same pending message is not
// re-notified on the next tick.
func TestWatchTick_DedupAfterSuccess(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess03"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief")
	statePath, st := watchTestState(t, sessionDir)
	fr := &recordingRunner{}

	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	assert.Len(t, fr.calls, 1, "the still-pending, already-notified message is not re-notified")
}

// Non-negotiable #4: the dedup survives a restart — a fresh state loaded from
// disk does not re-notify.
func TestWatchTick_RestartDoesNotReNotify(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess04"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief")
	statePath, st := watchTestState(t, sessionDir)

	fr1 := &recordingRunner{}
	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr1.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	require.Len(t, fr1.calls, 1)

	// "restart": load the state from disk and run again.
	st2, err := loadWatchState(statePath)
	require.NoError(t, err)
	fr2 := &recordingRunner{}
	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st2, statePath, watchTestCfg(), fr2.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	assert.Empty(t, fr2.calls, "a restarted watcher must not re-notify already-handled messages")
}

// A failed hook does NOT mark the message; it returns errHookFailed and, after
// the backoff, retries.
func TestWatchTick_HookFailureNoMarkThenRetries(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess05"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief")
	statePath, st := watchTestState(t, sessionDir)
	fr := &recordingRunner{failFirst: 1} // first call fails, the retry succeeds
	t0 := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	err := watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, t0, 65536, io.Discard, map[string]bool{})
	require.ErrorIs(t, err, errHookFailed, "a hook failure is reported as errHookFailed")
	assert.False(t, st.Entries["msg-aaaaaaaaaaaa"].OK, "a failed hook must not mark the message notified")

	// after the backoff (poll interval, attempts==1) it is a candidate again; the
	// retry now succeeds and marks it.
	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, t0.Add(2*time.Second), 65536, io.Discard, map[string]bool{}))
	assert.Len(t, fr.calls, 2, "after backoff the failed message is retried")
	assert.True(t, st.Entries["msg-aaaaaaaaaaaa"].OK, "the successful retry marks the message notified")
}

// A consumed message (no longer in inbox) has its marker pruned.
func TestWatchTick_PrunesConsumed(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess06"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief")
	statePath, st := watchTestState(t, sessionDir)
	fr := &recordingRunner{}

	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	require.Contains(t, st.Entries, "msg-aaaaaaaaaaaa")

	// the peer consumed it: remove from inbox, then tick again.
	require.NoError(t, os.Remove(filepath.Join(sessionDir, "inbox", "msg-aaaaaaaaaaaa.json")))
	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	assert.NotContains(t, st.Entries, "msg-aaaaaaaaaaaa", "a consumed message's marker is pruned")
}

// Acks are delivery receipts, not content — they never trigger a hook.
func TestWatchTick_AcksFiltered(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess07"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeAck, "ack")
	statePath, st := watchTestState(t, sessionDir)
	fr := &recordingRunner{}

	require.NoError(t, watchTick(context.Background(), sessionDir, sid, st, statePath, watchTestCfg(), fr.run, time.Now().UTC(), 65536, io.Discard, map[string]bool{}))
	assert.Empty(t, fr.calls, "an ack-only inbox triggers no hook")
}

// collectPendingForNotify logs a malformed file once per filename (the 24h
// watcher must not silently drop a message), and never logs it twice.
func TestCollectPendingForNotify_LogsMalformedOnce(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "nwsess08"
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, "inbox"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "inbox", "msg-bad.json"), []byte("{not json"), 0o600))

	var log strings.Builder
	warned := map[string]bool{}
	_, err := collectPendingForNotify(sessionDir, 65536, &log, warned)
	require.NoError(t, err)
	_, err = collectPendingForNotify(sessionDir, 65536, &log, warned)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(log.String(), "msg-bad.json"), "a malformed file is logged exactly once across ticks")
}

// --- execHookRunner: the real exec path ---

// writeHookScript writes an executable sh script that appends "$CAB_MSG_COUNT
// $CAB_MSG_IDS" to logFile and exits with exitCode.
func writeHookScript(t *testing.T, dir, logFile string, exitCode int) string {
	t.Helper()
	path := filepath.Join(dir, "hook.sh")
	script := "#!/bin/sh\nprintf '%s %s\\n' \"$CAB_MSG_COUNT\" \"$CAB_MSG_IDS\" >> \"" + logFile + "\"\nexit " + strconv.Itoa(exitCode) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func TestExecHookRunner_ArgvDirectRunsWithEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "hook.log")
	script := writeHookScript(t, dir, logFile, 0)

	cfg := watchConfig{hookTimeout: 5 * time.Second, hookArgv: []string{script}}
	run := execHookRunner(cfg, io.Discard)
	err := run(context.Background(), []string{"CAB_MSG_COUNT=2", "CAB_MSG_IDS=msg-a,msg-b"})
	require.NoError(t, err)

	data, rerr := os.ReadFile(logFile)
	require.NoError(t, rerr)
	assert.Contains(t, string(data), "2 msg-a,msg-b", "the hook ran with the injected env")
}

func TestExecHookRunner_NonZeroExitIsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := writeHookScript(t, dir, filepath.Join(dir, "hook.log"), 1)
	cfg := watchConfig{hookTimeout: 5 * time.Second, hookArgv: []string{script}}
	err := execHookRunner(cfg, io.Discard)(context.Background(), nil)
	require.Error(t, err, "a non-zero hook exit is an error → the batch is not marked")
}

func TestExecHookRunner_ShellMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "shell.log")
	cfg := watchConfig{hookTimeout: 5 * time.Second, shell: true, hookArgv: []string{"printf '%s\\n' \"$CAB_MSG_COUNT\" >> " + logFile}}
	err := execHookRunner(cfg, io.Discard)(context.Background(), []string{"CAB_MSG_COUNT=5"})
	require.NoError(t, err)
	data, rerr := os.ReadFile(logFile)
	require.NoError(t, rerr)
	assert.Contains(t, string(data), "5")
}

func TestExecHookRunner_TimeoutKills(t *testing.T) {
	t.Parallel()
	cfg := watchConfig{hookTimeout: 100 * time.Millisecond, hookArgv: []string{"sleep", "5"}}
	start := time.Now()
	err := execHookRunner(cfg, io.Discard)(context.Background(), nil)
	require.Error(t, err, "a hook exceeding --hook-timeout is killed and reported as an error")
	assert.Less(t, time.Since(start), 3*time.Second, "it is killed near the timeout, not after the full sleep")
}

// --- runNotifyWatch: early-return paths (flag validation, dry-run, guardrail) ---

func TestRunNotifyWatch_RequiresHook(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	err := runNotifyWatch([]string{"--session-id=nwrun001"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook command is required")
}

func TestRunNotifyWatch_InvalidWatchName(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	err := runNotifyWatch([]string{"--session-id=nwrun002", "--watch-name=bad/name", "--", "echo", "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "watch-name")
}

// --dry-run does one scan, prints, and exits without creating the lock/state.
func TestRunNotifyWatch_DryRun(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	sid := "nwrun003"
	plantRoleSession(t, dataDir, sid, session.RoleEsc)
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief")

	_, stderr := captureStdoutStderr(t, func() {
		err := runNotifyWatch([]string{"--session-id=" + sid, "--dry-run", "--", "echo", "wake"})
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "dry-run", "dry-run announces itself")
	assert.Contains(t, stderr, "hook would run")
	assert.NoFileExists(t, filepath.Join(dataDir, "sessions", sid, notifyWatchDir, "default.lock"), "dry-run takes no lock")
	assert.NoFileExists(t, filepath.Join(dataDir, "sessions", sid, notifyWatchDir, "default.json"), "dry-run writes no state")
	assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "inbox", "msg-aaaaaaaaaaaa.json"), "dry-run consumes nothing")
}

// Non-negotiable #5: a session actively in listen is a double-consumer; refuse
// without --allow-concurrent-consumer.
func TestRunNotifyWatch_GuardrailRefusesActiveListener(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	sid := "nwrun004"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-x", "/repo/x", "", "") // PID = os.Getpid() (live)
	mgr := session.NewManager(dataDir, time.Second)
	require.NoError(t, mgr.SetListenUntil(sid, time.Now().UTC().Add(time.Hour)))

	err := runNotifyWatch([]string{"--session-id=" + sid, "--", "echo", "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumer", "a live listener triggers the double-consumer guardrail")
}
