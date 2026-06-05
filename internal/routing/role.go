// Package routing implements role-based routing rules for cli-agents-bridge
// peer-to-peer messages. Resolves BUG-3 (Patil routing accepted any
// TARGET_ID without role semantic validation, allowing an ESC to message
// another ESC under the misconception it was VAL).
//
// The default policy is hub-and-spoke with VAL as the hub:
//   - val ↔ esc        OK (canonical workflow)
//   - val ↔ val        OK (multi-VAL planning)
//   - val ↔ architect  OK (triadic pattern)
//   - val ↔ observer   OK (val can notify observers)
//   - esc ↔ esc        REJECTED (must route through VAL) — override with
//     --allow-mesh for advanced multi-ESC scenarios
//   - observer → any   REJECTED (observers are read-only sinks)
//   - neutral ↔ any    OK (v1 schema compat — neutral is the read-default
//     for Patil v1 messages with no role field)
//
// The override is explicit by design (CLAUDE.md "no fallback impliciti"):
// callers wanting mesh peer-to-peer must pass --allow-mesh and accept the
// routing chaos risk Alan reported empirically.
package routing

import (
	"errors"
	"fmt"
)

// ErrEscToEscForbidden is returned by ValidateSendPair when an esc role
// attempts to message another esc without the allowMesh override. Caller
// should surface the error to stderr + exit 2 (validation) — the message
// must NOT be written to disk.
var ErrEscToEscForbidden = errors.New("esc→esc routing forbidden by default (use --allow-mesh to override)")

// ErrObserverCannotSend is returned when role=observer attempts to send.
// Observers receive events but do not originate messages.
var ErrObserverCannotSend = errors.New("observer role cannot send messages (observers are read-only sinks)")

// ValidateSendPair returns nil if a message with the given fromRole/toRole
// is allowed by the default routing policy, or an error otherwise.
//
// allowMesh, when true, relaxes the esc↔esc constraint. It does NOT relax
// the observer-cannot-send constraint (observers are structurally read-only,
// no flag can override).
//
// Unknown roles (anything outside val/esc/architect/observer/neutral) are
// permitted to send/receive — Sprint 3 keeps the policy permissive for
// forward compat with v0.3+ role additions. Validation is structural, not
// enumerated.
func ValidateSendPair(fromRole, toRole string, allowMesh bool) error {
	if fromRole == "observer" {
		return fmt.Errorf("%w: from=%q to=%q", ErrObserverCannotSend, fromRole, toRole)
	}
	if fromRole == "esc" && toRole == "esc" && !allowMesh {
		return fmt.Errorf("%w: from=%q to=%q", ErrEscToEscForbidden, fromRole, toRole)
	}
	return nil
}
