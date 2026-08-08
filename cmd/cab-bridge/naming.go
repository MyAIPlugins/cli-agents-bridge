package main

import (
	"path/filepath"
	"strings"
)

// Naming and peer-selection helpers, kept when bootstrap was removed in v0.8:
// join still derives a name the same way, and overview still needs the pairing
// counterpart. The rest of bootstrap went away with `listen`.

// selectPeer picks the peer that shapes this agent's name: the complementary
// role (esc->val, val->esc) when present, otherwise any peer of a DIFFERENT role
// (a same-role peer is not a pairing counterpart — two vals are independent
// orchestrators). Among candidates, most-recent first (LastHeartbeat desc, then
// StartedAt desc via session id tiebreak), matching findIdentityMatches'
// determinism. peers is already scope-filtered by the caller. Returns ok=false
// when no usable peer exists (a lone first bootstrap).
func selectPeer(myRole string, peers []peerSummary) (*peerSummary, bool) {
	complement := map[string]string{"esc": "val", "val": "esc"}[myRole]

	var best *peerSummary
	bestRank := func(p peerSummary) int {
		if complement != "" && p.Role == complement {
			return 2 // exact complementary role — strongest
		}
		if p.Role != myRole {
			return 1 // different role, not the complement — usable fallback
		}
		return 0 // same role — never a naming counterpart
	}
	bestScore := 0
	for i := range peers {
		p := peers[i]
		score := bestRank(p)
		if score == 0 {
			continue
		}
		if best == nil || score > bestScore ||
			(score == bestScore && p.LastHeartbeat.After(best.LastHeartbeat)) ||
			(score == bestScore && p.LastHeartbeat.Equal(best.LastHeartbeat) && p.SessionID > best.SessionID) {
			b := peers[i]
			best = &b
			bestScore = score
		}
	}
	return best, best != nil
}

// deriveAgentName computes this agent's name with zero config (F-40, the core).
//
// Rule:
//  1. If a peer's name matches "<PEER_ROLE_UPPER>-<suffix>" (e.g. peer role=val,
//     name "VAL-cab" -> suffix "cab"), inherit the suffix with MY role:
//     "<MY_ROLE_UPPER>-<suffix>" (e.g. "ESC-cab"). This is what makes two fresh
//     agents converge on a matching pair.
//  2. Otherwise (no peer, or a peer whose name is not "<ROLE>-..."), fall back to
//     "<MY_ROLE_UPPER>-<scopeBase>", a deterministic default.
//
// Because the default suffix is the scope basename, convergence holds in EITHER
// order: val-first -> "VAL-<base>", then esc sees it -> "ESC-<base>"; esc-first
// -> "ESC-<base>", then val sees it -> "VAL-<base>".
//
// Edge (VAL-ratified MVP, documented not hidden): if a peer appears or changes
// its name BETWEEN two bootstraps of the same agent, the derived name can drift
// (e.g. lone "ESC-<base>" then, peer appeared, "ESC-<suffix>") so the second
// bootstrap's --resume will not match the first session — a new one is created
// and the old is reclaimed by the 24h auto-gc. Rare (needs the peer to
// appear/change exactly between two bootstraps); a resume-by-disk-identity
// hardening is a tracked follow-up, deliberately not added here.
//
// peers holds at most the single selectPeer result; it is a slice so the helper
// stays pure (no selectPeer call inside) and trivially table-testable.
func deriveAgentName(myRole, scopeBase string, peers []peerSummary) (name, basis string) {
	myPrefix := roleUpper(myRole)
	if scopeBase == "" || scopeBase == "." || scopeBase == string(filepath.Separator) {
		scopeBase = "session" // never produce a bare "ESC-" on a degenerate scope
	}
	for _, p := range peers {
		// Inherit a suffix only from a DIFFERENT role. The convergence exists to
		// pair complements (VAL-x -> ESC-x); applied between two agents of the
		// same role it makes them converge on ONE name, which is the collision
		// this derivation is supposed to avoid (CRI2 P0).
		if p.Role == myRole {
			continue
		}
		peerPrefix := roleUpper(p.Role) + "-"
		if strings.HasPrefix(p.AgentName, peerPrefix) {
			suffix := strings.TrimPrefix(p.AgentName, peerPrefix)
			if suffix != "" {
				return myPrefix + "-" + suffix, "peer:" + p.SessionID
			}
		}
	}
	return myPrefix + "-" + scopeBase, "scope-basename"
}

// roleUpper renders a role as its name prefix: val->VAL, esc->ESC; any other
// role is uppercased as-is so observer/architect/neutral still produce a sane,
// stable prefix.
func roleUpper(role string) string {
	return strings.ToUpper(role)
}
