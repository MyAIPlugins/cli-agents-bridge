// Package session implements session lifecycle: registration, manifest
// management, longest-prefix lookup, heartbeat goroutine, and PID-based
// locking. Resolves BUG-1, BUG-3, BUG-5, BUG-6, BUG-9 (see PLAN §2).
package session

import (
	"fmt"
	"time"
)

// SchemaVersionV2 is the manifest schema version emitted by cli-agents-bridge
// v0.2.0. PLAN §4.3 trimmed YAGNI: 4 new fields vs Patil v1
// (schemaVersion, role, agentName, pid). Reading v1 manifests is supported
// elsewhere with default values applied (role="neutral", agentName=projectName,
// pid=0).
const SchemaVersionV2 = 2

// Valid roles in the role-based routing model (PLAN §4.3). The "neutral"
// value is the v1-read fallback, not a recommended runtime value.
const (
	RoleVal       = "val"
	RoleEsc       = "esc"
	RoleArchitect = "architect"
	RoleObserver  = "observer"
	RoleNeutral   = "neutral"
)

// Valid statuses in the manifest lifecycle. MVP uses only "active";
// additional states (idle, paused, terminating) land in v0.3+.
const (
	StatusActive = "active"
)

// Manifest is the on-disk representation of a session (schema v2).
//
// Layout matches PLAN §4.3 exactly. Field order is alphabetical-by-struct,
// not alphabetical-by-JSON (JSON marshaling preserves struct order, but
// readers should not depend on it).
type Manifest struct {
	SessionID     string    `json:"sessionId"`
	SchemaVersion int       `json:"schemaVersion"`
	ProjectName   string    `json:"projectName"`
	ProjectPath   string    `json:"projectPath"`
	AgentName     string    `json:"agentName"`
	Role          string    `json:"role"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"startedAt"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	Status        string    `json:"status"`
	Capabilities  []string  `json:"capabilities"`

	// LastConsumedMsgID is the ID of the most recently consumed inbox message
	// (moved to processed/ by listen, or matched by receive). Empty until the
	// session consumes its first message. F-12 observability: an orchestrator
	// reads this (via peers/status) to tell an idle session from one that is
	// actively draining its inbox. omitempty keeps it out of v1/legacy manifests
	// and of a never-consumed session's JSON.
	//
	// Note: a VAL orchestrator that does not consume via listen leaves this
	// empty by design — it pulls replies via receive and observes peers' acks in
	// its own inbox instead. An empty value is therefore NOT a bug.
	LastConsumedMsgID string `json:"lastConsumedMsgId,omitempty"`

	// TeamID isolates a VAL/ESC pair from other pairs sharing the same data dir
	// (F-5). Set via `register --team=<name>`. peers --team=<name> filters on it,
	// and whoami prints it so an agent can confirm which team/data dir it is in.
	// Optional and additive: empty means "no team" (the v1/legacy and
	// register-without-team default), so omitempty keeps it out of those
	// manifests and Validate/ApplyV1Defaults deliberately ignore it.
	TeamID string `json:"teamId,omitempty"`

	// Scope is the absolute project-root path this session belongs to (F-17),
	// derived automatically at register time via FindProjectRoot (the cwd's
	// nearest `.git` ancestor, else the cwd itself). peers filters on it by
	// default so a fresh session sees only its own project's pair with zero
	// config, and whoami prints it. Distinct from the manual TeamID override:
	// scope is the automatic structural identity, teamId the manual knob for the
	// cases scope alone cannot separate (LL-7). Optional and additive: empty
	// means "no scope" (v1/legacy and pre-F-17 manifests), so omitempty keeps it
	// out of those and Validate/ApplyV1Defaults deliberately ignore it. Never
	// used as a filesystem path component — only string-compared for filtering —
	// so it needs no SC-4-style validation.
	Scope string `json:"scope,omitempty"`
}

// Validate checks that the manifest has the minimum required fields for
// runtime safety. SessionID and ProjectPath are non-negotiable: missing
// either indicates a corrupt or hand-crafted manifest we should not trust.
func (m *Manifest) Validate() error {
	if m.SessionID == "" {
		return fmt.Errorf("manifest: empty sessionId")
	}
	if m.ProjectPath == "" {
		return fmt.Errorf("manifest: empty projectPath (sessionId=%s)", m.SessionID)
	}
	if m.SchemaVersion != SchemaVersionV2 && m.SchemaVersion != 1 {
		return fmt.Errorf("manifest: unsupported schemaVersion=%d (sessionId=%s, supported: 1, 2)", m.SchemaVersion, m.SessionID)
	}
	return nil
}

// ApplyV1Defaults populates v2-only fields with safe defaults when reading
// a v1 manifest (PLAN §4.3 backward-compat read). Called by manager on read
// when m.SchemaVersion == 1.
func (m *Manifest) ApplyV1Defaults() {
	if m.Role == "" {
		m.Role = RoleNeutral
	}
	if m.AgentName == "" {
		m.AgentName = m.ProjectName
	}
	// PID stays 0 — there is no safe inference for a v1 manifest's owning
	// process. Lock acquisition logic must handle PID=0 as "no lock holder
	// inferable, treat as stale" (PLAN §9 SC-6).
}
