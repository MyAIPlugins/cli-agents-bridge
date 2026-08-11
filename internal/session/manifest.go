// Package session implements session lifecycle: registration, manifest
// management, longest-prefix lookup, heartbeat goroutine, and PID-based
// locking. Resolves BUG-1, BUG-3, BUG-5, BUG-6, BUG-9 (see PLAN §2).
package session

import (
	"fmt"
	"strings"
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
	RoleCritic    = "critic"
	RoleArchitect = "architect"
	RoleObserver  = "observer"
	RoleNeutral   = "neutral"
)

// RoleChoice is a role together with the one line shown wherever roles are
// offered. Description travels WITH the name because the two drifted apart the
// moment they lived in different files: `join --role` advertised four roles,
// `register --role` five, and the help text a fourth list of its own.
type RoleChoice struct {
	Name        string
	Description string
}

// SelectableRoles is the single source for every list of roles a human or an
// agent is shown, in display order.
//
// NOT a whitelist. Routing is permissive by construction (see
// routing.ValidateSendPair): an unknown role sends and receives normally, which
// is what let `critic` work before it was ever declared here. This list decides
// what gets OFFERED, and being offered is what makes a role findable — a fresh
// critic reading `--role` help that omits `critic` picks something else and
// walks into a wall the routing never built.
//
// `neutral` is deliberately absent: it is the v1-read fallback, not a choice.
var SelectableRoles = []RoleChoice{
	{RoleVal, "orchestrates and hands out work"},
	{RoleEsc, "executes it"},
	{RoleCritic, "reviews and criticises — the CRI role; reports to its val and sends to nobody else"},
	{RoleArchitect, "reserved for Claude Desktop, which joins through the MCP connector"},
	{RoleObserver, "reads only, never sends"},
}

// roleNames renders the selectable roles as "val|esc|critic|..." — values only,
// no annotation, because a token on a copyable surface must be a usable value.
//
// UNEXPORTED on purpose, and that is the point rather than an accident: the bare
// list cannot be printed from outside this package, so no surface can offer the
// roles without also carrying the reservation. Exporting it was how the same
// list came to be fixed in one place and left behind in another three times in
// one day — the last time losing the mark entirely on the error that teaches,
// which is the worst of the four because it is the recovery path from a mistake.
//
// The property is now structural: there is one way to render the list, and it
// includes the note.
func roleNames() string {
	names := make([]string, 0, len(SelectableRoles))
	for _, r := range SelectableRoles {
		names = append(names, r.Name)
	}
	return strings.Join(names, "|")
}

// RoleNamesWithNote is the ONLY way to render the role list outside this
// package: the values, plus the reservation said BESIDE the list and never
// inside it.
//
// The first attempt put the mark in the list itself — `architect(reserved)` —
// and that token is a value like any other on a surface built to be copied, in a
// parser that accepts any string: pasting it produced a session whose role was
// literally "architect(reserved)", outside every role invariant, silently and
// with exit 0. A list of values must contain only values.
func RoleNamesWithNote() string {
	return roleNames() + "  (architect is reserved for Claude Desktop over MCP)"
}

// RoleLines renders the selectable roles as an indented block, one per line,
// for an error that has to teach rather than list.
func RoleLines() string {
	var b strings.Builder
	width := 0
	for _, r := range SelectableRoles {
		if len(r.Name) > width {
			width = len(r.Name)
		}
	}
	for _, r := range SelectableRoles {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, r.Name, r.Description)
	}
	return b.String()
}

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

	// State is the agent task-state (F-23a): one of the State* constants
	// (idle/working/done/orchestrating), set by the agent via `cab-bridge state
	// <value>` and shown by peers/status/whoami. Distinct from Status (session
	// lifecycle "active") — it answers "what is the agent doing". State
	// "orchestrating" makes the session heartbeat-exempt in session.IsStale.
	// Optional and additive: empty means "unknown" (legacy/pre-F-23 and
	// never-set), so omitempty keeps it out of those manifests and
	// Validate/ApplyV1Defaults deliberately ignore it. Read paths are lenient
	// (any value displayed verbatim, forward-compat); only the setter validates
	// against the canonical set.
	State string `json:"state,omitempty"`

	// FormerAgentNames are the names this session answered to before being
	// renamed, oldest first. They route nothing: the only reader is the
	// "no agent named X" error, which uses them to say where that name went
	// instead of listing everyone and leaving the caller to guess.
	//
	// It exists because a rename is invisible to a peer that was not watching —
	// and a peer that was OFFLINE during the rename could not have been told by
	// any notification either. A fact on disk reaches both.
	FormerAgentNames []string `json:"formerAgentNames,omitempty"`

	// No listenUntil, no waitingSince. Both described a wait from the manifest,
	// and both stopped being true there: a wait has no deadline (§2.2 rev.
	// cdb21dc) so nothing wrote listenUntil after `listen` went away, and
	// waitingSince was cleared by whichever `next` exited last — including one
	// that had just been evicted by the instance whose wait it then erased.
	//
	// Who is waiting, and since when, is answered by listener.json instead: it is
	// mutated only under the session lock, carries the owner's PID and ClaimedAt,
	// and an instance that exits writes nothing to it. See ListenerOwner.Listening.
	//
	// Both fields may still exist in manifests on disk; ReadJSON ignores unknown
	// fields, and the first save drops them.

	// LastReclaim, when non-nil, reports what a `register --resume` RECLAIM just
	// superseded (B-2). It is set IN-MEMORY by tryReuse on the returned manifest
	// and read by the cmd layer for the reclaim output. json:"-": it is NEVER
	// persisted (it describes a single operation, not session state) and
	// LoadManifest never populates it — distinct from the on-disk listener.json
	// ownership record. Kept off Register's signature (30 call-sites) by riding
	// on the manifest the call already returns.
	LastReclaim *ReclaimInfo `json:"-"`
}

// WasResumed reports whether the Register call that returned this manifest
// RESUMED an existing session rather than creating one.
//
// It exists so that no caller has to infer the answer, which is how two of them
// got it wrong: `register` and `join` both decided by comparing StartedAt with
// the wall clock taken just before the call. StartedAt was persisted in an
// earlier life, so it says when the session BEGAN, not what this call did — and
// with a manifest whose StartedAt lies in the future (clock rollback, a restored
// VM, a file from another machine) both printed the opposite of what happened:
// a derivation that never occurred, and `registered-new` for a session that was
// reclaimed. Reproduced on the binary (CRI re-gate).
//
// The distinction is the temporal form of "which object is this a property of":
// a persisted field is evidence about THEN. LastReclaim is set in memory by
// tryReuse on this very call and never read from disk, so it is evidence about
// NOW — which is the question.
//
// A method rather than the expression at each site, so the translation from
// "reclaimed something" to "was a resume" lives in ONE place: if the two ever
// stop coinciding, there is a single line to change instead of a search.
func (m *Manifest) WasResumed() bool { return m.LastReclaim != nil }

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
		// SANITISED, because this is the FOURTH pen that writes AgentName — after
		// Register, RenameAgent and Register's derived default — and the one
		// nobody counted: a v1 manifest in a directory containing the scope
		// separator produced an unaddressable name at every load, from the new
		// binary, today (CRI2 F-3b). ProjectName is a basename like any other.
		//
		// FROZEN ON THE @-ONLY ALGORITHM, deliberately, and not to be "aligned"
		// with SanitizeDerivedName later (F-124, CRI diff-gate P1-1).
		//
		// This is the only pen on a READ path, and a read here is not free: it
		// runs inside LoadManifest, and touchHeartbeat is load-modify-save
		// (manager.go:586), so whatever this computes is PERSISTED at the first
		// heartbeat — with no FormerAgentNames, because from the disk's point of
		// view there was no previous name to record.
		//
		// The argument for widening it was that an absent AgentName means the
		// session never had a name, so nothing is being renamed. That confuses
		// the field with the identity: the derived value is what `peers` showed
		// and what the resolver matched, so it WAS the name, and widening the
		// repair would change it under peers that still hold the old one — once,
		// silently, and irreversibly at the next write. A v1 name stays exactly
		// as addressable, or unaddressable, as it has always been.
		m.AgentName = strings.ReplaceAll(m.ProjectName, ScopeSeparator, derivedNameReplacement)
	}
	// PID stays 0 — there is no safe inference for a v1 manifest's owning
	// process. Lock acquisition logic must handle PID=0 as "no lock holder
	// inferable, treat as stale" (PLAN §9 SC-6).
}
