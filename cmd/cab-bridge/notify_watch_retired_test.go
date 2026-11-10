//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-160: the watcher's lock lives inside the session directory, and cleanup unlinks
// it while the watcher still holds it. The lock cannot say "your session is gone",
// so the loop asks the manifest on every tick.

const watchRetiredPoll = 50 * time.Millisecond

func watchRetiredParams(t *testing.T, dataDir, sid string, logw *bytes.Buffer) watchLoopParams {
	t.Helper()
	sessionDir := filepath.Join(dataDir, "sessions", sid)
	statePath, st := watchTestState(t, sessionDir)
	cfg := watchTestCfg()
	cfg.pollInterval = watchRetiredPoll
	runner := &recordingRunner{}
	return watchLoopParams{
		mgr: session.NewManager(dataDir, time.Second), sid: sid, sessionDir: sessionDir,
		statePath: statePath, st: st, cfg: cfg, run: runner.run, guard: noGuard,
		maxBytes: 65536, logw: logw, warned: map[string]bool{},
	}
}

func TestWatchLoop_ExitsWhenTheSessionIsRetired(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	const sid = "nwret001"
	plantLiveSession(t, dataDir, sid)
	var logw bytes.Buffer
	p := watchRetiredParams(t, dataDir, sid, &logw)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, p) }()

	select {
	case err := <-done:
		t.Fatalf("the watcher stopped on a live session: %v", err)
	case <-time.After(4 * watchRetiredPoll):
	}

	require.NoError(t, os.Remove(filepath.Join(p.sessionDir, "manifest.json")))
	retiredAt := time.Now()

	select {
	case err := <-done:
		require.ErrorIs(t, err, session.ErrSessionGone)
		assert.Contains(t, err.Error(), "no longer exists")
		assert.Less(t, time.Since(retiredAt), 2*time.Second, "one tick is the longest it keeps running")
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher is still running after the session was retired")
	}
}

// A manifest that is there but cannot be read right now is not a retirement: the
// watcher says so and keeps watching, because stopping on a transient error would
// turn a hiccup into a dead watcher nobody notices.
func TestWatchLoop_KeepsWatchingWhenTheManifestCannotBeRead(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	const sid = "nwret002"
	plantLiveSession(t, dataDir, sid)
	var logw bytes.Buffer
	p := watchRetiredParams(t, dataDir, sid, &logw)
	require.NoError(t, os.WriteFile(filepath.Join(p.sessionDir, "manifest.json"), []byte("{not json"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, p) }()

	select {
	case err := <-done:
		t.Fatalf("the watcher stopped on an unreadable manifest: %v", err)
	case <-time.After(6 * watchRetiredPoll):
	}
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "and it ends normally when asked to")
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop when its context was cancelled")
	}
	// logw is read after the loop returned, so there is no concurrent writer.
	assert.Contains(t, logw.String(), "could not read the manifest")
}
