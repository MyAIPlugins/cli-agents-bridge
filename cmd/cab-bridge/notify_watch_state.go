package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// notifyWatchBackoffMaxFactor caps the retry backoff at pollInterval ×8, so a
// persistently-failing hook is retried on a widening interval (poll, 2×, 4×, 8×)
// without ever hammering it every tick (non-negotiable #4: "backoff, not a tight
// loop") and without ever giving up (the watcher stays operational).
const notifyWatchBackoffMaxFactor = 8

// watchEntry is the persistent dedup record for ONE message id (F-66). A
// message is "notified" only once its hook exited 0 (OK=true) — so the watcher
// never re-notifies it, and survives a restart without re-spamming (markers live
// on disk). A failed hook leaves OK=false with a growing Attempts/LastError, so
// the id stays a candidate, retried on a backoff keyed off LastAttempt.
type watchEntry struct {
	OK          bool   `json:"ok"`                    // hook exited 0 → notified, never re-notify
	Attempts    int    `json:"attempts"`              // hook attempts so far (success or failure)
	LastAttempt string `json:"lastAttempt,omitempty"` // RFC3339Nano of the last hook attempt
	LastError   string `json:"lastError,omitempty"`   // last hook failure message (cleared on success)
}

// watchState is the on-disk dedup map for a single notify-watch instance
// (sessions/<sid>/notify-watch/<watch-name>.json). Persistent so a restarted
// watcher does not re-notify already-handled messages (non-negotiable #4).
type watchState struct {
	Entries map[string]*watchEntry `json:"entries"`
}

// loadWatchState reads the persisted state, returning an empty (non-nil) state
// when the file does not exist yet (first run). A malformed state file is a hard
// error — silently starting from empty would re-notify everything, the spam the
// persistent dedup exists to prevent.
func loadWatchState(path string) (*watchState, error) {
	st := &watchState{Entries: map[string]*watchEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		return nil, fmt.Errorf("notify-watch: read state %q: %w", path, err)
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("notify-watch: parse state %q (delete it to reset): %w", path, err)
	}
	if st.Entries == nil {
		st.Entries = map[string]*watchEntry{}
	}
	return st, nil
}

// save persists the state atomically (SC-5 temp+fsync+rename, mode 0o600).
func (s *watchState) save(path string) error {
	return transportfs.AtomicWriteJSON(path, s)
}

// entry returns the (lazily created) record for msgID.
func (s *watchState) entry(msgID string) *watchEntry {
	if s.Entries == nil {
		s.Entries = map[string]*watchEntry{}
	}
	e := s.Entries[msgID]
	if e == nil {
		e = &watchEntry{}
		s.Entries[msgID] = e
	}
	return e
}

// shouldNotify reports whether msgID is a candidate for the hook this tick:
// never-seen ids and failed ids whose backoff has elapsed are due; a
// successfully-notified id (OK) is never re-notified. A markered id with an
// unparseable LastAttempt is conservatively retried (better a duplicate
// notification than a silently-dropped wake).
func (s *watchState) shouldNotify(msgID string, pollInterval time.Duration, now time.Time) bool {
	e := s.Entries[msgID]
	if e == nil {
		return true
	}
	if e.OK {
		return false
	}
	if e.Attempts <= 0 {
		return true
	}
	last, err := time.Parse(time.RFC3339Nano, e.LastAttempt)
	if err != nil {
		return true
	}
	return !now.Before(last.Add(notifyRetryBackoff(pollInterval, e.Attempts)))
}

// markSuccess records that the hook fired successfully for msgID — it will not
// be notified again (across restarts, since state is on disk).
func (s *watchState) markSuccess(msgID string, now time.Time) {
	e := s.entry(msgID)
	e.OK = true
	e.Attempts++
	e.LastAttempt = now.UTC().Format(time.RFC3339Nano)
	e.LastError = ""
}

// markFailure records a failed hook attempt for msgID: OK stays false so the id
// remains a candidate, and Attempts drives the widening backoff.
func (s *watchState) markFailure(msgID, errMsg string, now time.Time) {
	e := s.entry(msgID)
	e.OK = false
	e.Attempts++
	e.LastAttempt = now.UTC().Format(time.RFC3339Nano)
	e.LastError = errMsg
}

// prune drops markers for ids no longer pending in inbox/ (the peer consumed
// them via receive/listen), so the state file does not grow unbounded. Returns
// true if it removed anything, so the caller can avoid an unnecessary disk write
// on an idle tick (P1.1 idle write-storm fix).
func (s *watchState) prune(present map[string]bool) bool {
	changed := false
	for id := range s.Entries {
		if !present[id] {
			delete(s.Entries, id)
			changed = true
		}
	}
	return changed
}

// notifyRetryBackoff returns the minimum wait before retrying a failed hook for
// an id with the given attempt count: pollInterval, then 2×, 4×, 8× (capped).
// attempts<=1 → one poll interval (the first retry is just the next tick).
func notifyRetryBackoff(pollInterval time.Duration, attempts int) time.Duration {
	factor := 1
	for i := 1; i < attempts && factor < notifyWatchBackoffMaxFactor; i++ {
		factor *= 2
	}
	if factor > notifyWatchBackoffMaxFactor {
		factor = notifyWatchBackoffMaxFactor
	}
	return pollInterval * time.Duration(factor)
}
