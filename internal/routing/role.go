// Package routing implements role-based routing rules for cli-agents-bridge
// peer-to-peer messages. Resolves BUG-3 (Patil routing accepted any
// TARGET_ID without role semantic validation, allowing an ESC to message
// another ESC under the misconception it was VAL).
//
// Three rules are enforced. EVERYTHING ELSE IS ALLOWED — the policy is a set of
// prohibitions, not a table of permissions, and the list below describes what
// that produces rather than adding to it:
//
//   - observer → any   REJECTED, structurally: observers are read-only sinks
//     and no flag relaxes it.
//
//   - esc ↔ esc        REJECTED by default, --allow-mesh relaxes it. Two
//     executors on one task produce conflicting EDITS; that is a hazard of
//     writing, which is why it is the only pairing singled out.
//
//   - val ↔ esc        OK (canonical workflow)
//
//   - val ↔ val        OK (multi-VAL planning)
//
//   - val ↔ critic     OK (the triadic pattern: a critic reports to a val)
//
//   - critic → anyone but a val  REJECTED, structurally, no flag. A critic
//     talks to its val and to nobody else: see ValidateSendPair for why this is
//     not the esc↔esc rule wearing a different name.
//
//   - val ↔ architect  OK — architect is RESERVED for Claude Desktop joining
//     through the MCP connector (F-72), not a synonym for critic.
//
//   - val ↔ observer   OK (val can notify observers)
//
//   - neutral ↔ any    OK (v1 schema compat — neutral is the read-default
//     for Patil v1 messages with no role field)
//
// A role absent from this list still sends and receives: see ValidateSendPair.
// That permissiveness is why `critic` worked for a whole arc before it was
// declared anywhere — the wall a fresh critic hit was never in the routing, it
// was in the list of roles it was OFFERED.
//
// The override is explicit by design (docs/dev-conventions.md "No implicit
// fallbacks"):
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

// ErrCriticMustGoThroughVal is returned when a critic addresses anyone but a
// val. It carries the way FORWARD, not just the refusal: a critic hitting this
// is trying to deliver a finding, and an error that only forbids leaves it
// holding one.
var ErrCriticMustGoThroughVal = errors.New(
	"a critic sends only to its val — tell the val instead and it will pass it on")

// ValidateSendPair returns nil if a message with the given fromRole/toRole
// is allowed by the default routing policy, or an error otherwise.
//
// allowMesh, when true, relaxes the esc↔esc constraint AND NOTHING ELSE. The
// other two are structural (LL-7): no flag makes an observer send, and no flag
// lets a critic route around its val.
//
// Unknown roles are permitted to send/receive — the policy is permissive for
// forward compat with later role additions. Validation is structural, not
// enumerated.
func ValidateSendPair(fromRole, toRole string, allowMesh bool) error {
	if fromRole == "observer" {
		return fmt.Errorf("%w: from=%q to=%q", ErrObserverCannotSend, fromRole, toRole)
	}
	// A critic talks to its val and to nobody else. This is NOT the esc↔esc rule
	// with different names on it: that one guards the CODE against two writers
	// and is therefore relaxable by an operator who accepts the risk. This one
	// guards the critic's INDEPENDENCE, which is the entire reason the role
	// exists — two critics who confer converge into a single voice, which is the
	// echo chamber a second opinion is bought to avoid (LL-15: the value is in
	// two DIFFERENT blind spots, and a channel between them merges the two).
	//
	// critic→esc is refused for a second reason: it bypasses the val, and the val
	// is where a finding gets VERIFIED before it becomes work. That step is not
	// ceremony — it is the gate, and it has caught wrong findings.
	//
	// It says "a val", not "its val", deliberately. The constraint is expressible
	// on ROLES alone, so it needs no notion of pairing — and there is no such
	// relation on disk to consult. With two vals in one scope a critic may address
	// either, which is a visible choice made by whoever types the name, not an
	// ambiguity the router resolves silently. Binding a critic to one val would
	// mean inventing and persisting a relationship; if that is ever needed it
	// should be designed, not guessed at here.
	if fromRole == "critic" && toRole != "val" {
		return fmt.Errorf("%w (tried to send to a %q)", ErrCriticMustGoThroughVal, toRole)
	}
	if fromRole == "esc" && toRole == "esc" && !allowMesh {
		return fmt.Errorf("%w: from=%q to=%q", ErrEscToEscForbidden, fromRole, toRole)
	}
	return nil
}
