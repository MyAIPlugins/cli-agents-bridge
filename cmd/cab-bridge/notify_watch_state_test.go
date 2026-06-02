package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatchState_SuccessThenNeverReNotify(t *testing.T) {
	t.Parallel()
	st := &watchState{Entries: map[string]*watchEntry{}}
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	assert.True(t, st.shouldNotify("msg-aaaaaaaaaaaa", time.Second, now), "a never-seen id is a candidate")
	st.markSuccess("msg-aaaaaaaaaaaa", now)
	assert.False(t, st.shouldNotify("msg-aaaaaaaaaaaa", time.Second, now.Add(time.Hour)), "a notified id is never a candidate again")
}

// The persistent dedup: a success survives a save+load (restart) — the watcher
// must not re-notify after a restart.
func TestWatchState_DedupSurvivesReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "default.json")

	st := &watchState{Entries: map[string]*watchEntry{}}
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	st.markSuccess("msg-aaaaaaaaaaaa", now)
	require.NoError(t, st.save(path))

	reloaded, err := loadWatchState(path)
	require.NoError(t, err)
	assert.False(t, reloaded.shouldNotify("msg-aaaaaaaaaaaa", time.Second, now.Add(time.Hour)), "dedup marker persists across restart")
}

func TestWatchState_MissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	st, err := loadWatchState(filepath.Join(t.TempDir(), "nope.json"))
	require.NoError(t, err, "a missing state file is first-run, not an error")
	assert.Empty(t, st.Entries)
}

// A failed hook keeps the id a candidate (OK stays false), but only after the
// backoff has elapsed — not on the very next tick.
func TestWatchState_FailureBacksOffThenRetries(t *testing.T) {
	t.Parallel()
	st := &watchState{Entries: map[string]*watchEntry{}}
	poll := 10 * time.Second
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	st.markFailure("msg-aaaaaaaaaaaa", "boom", now)
	assert.False(t, st.Entries["msg-aaaaaaaaaaaa"].OK, "a failure must not mark the id notified")
	// Attempts==1 → backoff is one poll interval.
	assert.False(t, st.shouldNotify("msg-aaaaaaaaaaaa", poll, now.Add(5*time.Second)), "within the backoff window it is not yet due")
	assert.True(t, st.shouldNotify("msg-aaaaaaaaaaaa", poll, now.Add(11*time.Second)), "after the backoff it is due again")
}

func TestWatchState_Prune(t *testing.T) {
	t.Parallel()
	st := &watchState{Entries: map[string]*watchEntry{}}
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	st.markSuccess("msg-aaaaaaaaaaaa", now)
	st.markSuccess("msg-bbbbbbbbbbbb", now)

	st.prune(map[string]bool{"msg-aaaaaaaaaaaa": true}) // bbbb no longer pending
	assert.Contains(t, st.Entries, "msg-aaaaaaaaaaaa")
	assert.NotContains(t, st.Entries, "msg-bbbbbbbbbbbb", "a marker for a consumed id is pruned")
}

func TestNotifyRetryBackoff_GrowsAndCaps(t *testing.T) {
	t.Parallel()
	poll := time.Second
	assert.Equal(t, poll, notifyRetryBackoff(poll, 1), "first retry waits one poll interval")
	assert.Equal(t, 2*poll, notifyRetryBackoff(poll, 2))
	assert.Equal(t, 4*poll, notifyRetryBackoff(poll, 3))
	assert.Equal(t, 8*poll, notifyRetryBackoff(poll, 4))
	assert.Equal(t, 8*poll, notifyRetryBackoff(poll, 9), "backoff is capped at 8× the poll interval")
}

// A markered id with a corrupt LastAttempt is conservatively retried (a
// duplicate notification beats a silently-dropped wake).
func TestWatchState_CorruptLastAttemptRetries(t *testing.T) {
	t.Parallel()
	st := &watchState{Entries: map[string]*watchEntry{
		"msg-aaaaaaaaaaaa": {OK: false, Attempts: 2, LastAttempt: "not-a-time"},
	}}
	assert.True(t, st.shouldNotify("msg-aaaaaaaaaaaa", time.Second, time.Now().UTC()))
}
