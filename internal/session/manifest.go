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
