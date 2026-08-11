package main

import (
	"path/filepath"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
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
	// A critic's counterpart is the val it reports to — same shape as esc→val.
	// Without this a critic's overview paired it with whoever came first among
	// the other roles, which is not wrong so much as arbitrary.
	complement := map[string]string{
		session.RoleEsc:    session.RoleVal,
		session.RoleVal:    session.RoleEsc,
		session.RoleCritic: session.RoleVal,
	}[myRole]

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
func deriveAgentName(myRole, dirBase string, peers []peerSummary) (name, basis string) {
	// ALWAYS the caller's own working directory. No inheritance from a peer.
	//
	// The convergence existed when the suffix was the PROJECT (VAL-bridge ->
	// ESC-bridge), where copying it meant "same team". Now that the suffix is a
	// directory — which is what made the name injective — copying it would put
	// SOMEBODY ELSE'S directory into your name: an esc started in escdir would
	// be called ESC-valdir. The name would stop being a fact, and the stop-and-ask
	// that reasons in terms of directories ("lives in valdir — not this one")
	// would be reasoning over names that lie about directories.
	//
	// Injectivity comes for free: two sessions in one directory are already
	// refused (F-90), and who pairs with whom is visible in `peers` and in the
	// scope — it never needed encoding in a string.
	myPrefix := roleUpper(myRole)
	// REDUNDANT since F-124, and kept on purpose: SanitizeDerivedName now
	// guarantees a non-empty result and its only caller here passes through it,
	// so no degenerate base can reach this line. Left as defence in depth — but
	// SAID, because a guard whose comment does not admit it is unreachable is the
	// shape that made a listenUntil check silently always-false: the next reader
	// cannot tell a live defence from a leftover.
	if dirBase == "" || dirBase == "." || dirBase == string(filepath.Separator) {
		dirBase = "session" // never produce a bare "ESC-" on a degenerate path
	}
	return myPrefix + "-" + dirBase, "working-dir"
}

func roleUpper(role string) string {
	return strings.ToUpper(role)
}

// findRenamed reports the session that used to answer to `name`, so an error
// about an unknown recipient can say where that name went instead of listing
// everyone and leaving the caller to guess.
//
// Most-recent heartbeat first: a name can be handed down (an agent renames away
// from it, a later one adopts and leaves it too), and the useful answer is the
// last session that carried it, not the first one found on disk.
func findRenamed(mgr *session.Manager, peers []peerSummary, name string) (peerSummary, bool) {
	var best peerSummary
	found := false
	for _, p := range peers {
		mf, err := mgr.LoadManifest(p.SessionID)
		if err != nil {
			continue
		}
		if !contains(mf.FormerAgentNames, name) {
			continue
		}
		if !found || p.LastHeartbeat.After(best.LastHeartbeat) {
			best, found = p, true
		}
	}
	return best, found
}
