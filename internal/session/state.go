package session

import (
	"strings"
	"time"
)

// Agent task-state values (F-23a). Distinct from Status (session lifecycle,
// "active") and from a message's ProcessingState: State answers "what is the
// agent doing", set by the agent via `cab-bridge state <value>` and shown by
// peers/status/whoami. The empty string is the legacy/pre-F-23 value and means
// "unknown" — it carries no new semantics and behaves exactly as before.
const (
	StateIdle          = "idle"          // registered/listening, not working a task
	StateWorking       = "working"       // executing a task (head-down, outside listen)
	StateDone          = "done"          // task finished — readable by the VAL without the reply
	StateOrchestrating = "orchestrating" // an orchestrator (VAL), not in listen by design; heartbeat-exempt
)

// validStates is the canonical set accepted by the `state` setter. Read paths
// stay lenient (any string is displayed verbatim) for forward-compatibility, so
// this set guards WRITES only — a typo is rejected at set time, but a newer
// peer's future state value never breaks our read.
var validStates = map[string]struct{}{
	StateIdle: {}, StateWorking: {}, StateDone: {}, StateOrchestrating: {},
}

// IsValidState reports whether s is one of the canonical agent states. The empty
// string is NOT valid for an explicit set (the setter requires a value); it is
// only the implicit legacy/unknown default already on disk.
func IsValidState(s string) bool {
	_, ok := validStates[s]
	return ok
}

// StatesHint returns the canonical states comma-separated, for help and error
// messages. Order is fixed (idle, working, done, orchestrating) for stable output.
func StatesHint() string {
	return strings.Join([]string{StateIdle, StateWorking, StateDone, StateOrchestrating}, ", ")
}

// IsStale reports whether a session should be considered stale — its owning
// agent presumed gone — given its manifest, the StaleSeconds threshold, and the
// current time. It is the SINGLE source of truth for staleness, shared by
// peers, status, and cleanup's globalSweep so the displayed "stale" and the
// swept "stale" can never diverge (the two were computed inline and divergently
// before F-23a).
//
// A session whose State is "orchestrating" is heartbeat-EXEMPT and never stale:
// an orchestrator (a VAL) does not run a long-lived `listen`, so it has no
// heartbeat goroutine and would otherwise show stale within StaleSeconds even
// while actively working a gate — the "orchestrator looks dead" pain. The
// exemption is pure state-based and intentional (F-23a, ratified option A).
//
// ACCEPTED TRADE-OFF: a session that crashes WHILE orchestrating appears alive
// forever (no heartbeat ever unmasks it). This is acceptable because the state
// is an explicit self-declaration and a doubting observer verifies liveness via
// PID/git (the established ground-truth discipline). If crash-invisibility ever
// bites in real use, the clean upgrade path is a generous TTL (state-exempt only
// up to an OrchestratingTTLSeconds, then stale) — deferred as YAGNI for now.
//
// Note: this is the StaleSeconds (5-min display) staleness only. The 24h
// PID-dead auto-gc (internal/cleanup/gc.go) is a separate, stronger abandonment
// criterion and is deliberately NOT exempted — a session declared orchestrating
// but truly gone for >24h is still reclaimed.
func IsStale(m *Manifest, staleSeconds int, now time.Time) bool {
	if m.State == StateOrchestrating {
		return false
	}
	return now.Sub(m.LastHeartbeat) > time.Duration(staleSeconds)*time.Second
}
