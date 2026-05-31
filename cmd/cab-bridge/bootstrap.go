package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// bootstrapReport is the JSON summary emitted on stdout for a bootstrap that
// does NOT hand off to listen (a val, or an esc with --no-listen). It is
// self-documenting: a fresh agent learns what happened — its identity, whether
// it resumed or registered new, which peer (if any) shaped its name — without
// any follow-up exploration. An esc that enters listen prints this same summary
// to STDERR instead, keeping stdout clean for the listen NDJSON payload.
type bootstrapReport struct {
	SessionID   string             `json:"sessionId"`
	AgentName   string             `json:"agentName"`
	Role        string             `json:"role"`
	Scope       string             `json:"scope,omitempty"`
	Action      string             `json:"action"`      // "resumed" | "registered-new"
	NamingBasis string             `json:"namingBasis"` // "peer:<id>" | "scope-basename" | "override"
	Peer        *bootstrapPeerInfo `json:"peer"`        // null when no peer discovered
	State       string             `json:"state,omitempty"`
	Listening   bool               `json:"listening"`
}

// bootstrapPeerInfo is the discovered peer slimmed to what a fresh agent needs
// to know it is correctly paired.
type bootstrapPeerInfo struct {
	SessionID string `json:"sessionId"`
	AgentName string `json:"agentName"`
	Role      string `json:"role"`
	Stale     bool   `json:"stale"`
}

// runBootstrap is the F-40 one-shot, zero-config pairing command. A fresh agent
// runs `cab-bridge bootstrap --role=<val|esc>` and the binary, IN-PROCESS:
//
//   - resolves its project-root scope (default data dir + auto-scope, F-17 — no
//     CAB_DATA_DIR/--team to type);
//   - discovers an already-registered peer in that scope via collectPeers (a Go
//     []peerSummary, never a parsed stdout pipe — so the F-16-reinforced harness
//     output pollution can never corrupt the riskiest phase, by design);
//   - derives its own agent-name adaptively (inherits the peer's suffix, or
//     falls back to <ROLE>-<scope-basename>) so two fresh agents converge on a
//     matching VAL-x/ESC-x pair in either order, with zero config;
//   - registers idempotently (--resume, F-27): a stable peer means the same
//     derived name means the same identity means a resume of the prior session;
//   - for role=esc, hands off to listen --wait-one (native wake) unless
//     --no-listen; for role=val, sets state=orchestrating (so the orchestrator
//     is observable, not falsely stale, from its first command) and exits.
func runBootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	role := fs.String("role", "", "this agent's role (required): val|esc (others register without pairing magic)")
	agentNameOverride := fs.String("agent-name", "", "override the adaptive name derivation (escape hatch); empty = derive")
	projectPath := fs.String("project-path", "", "project root path (default: cwd) — also the test injection point")
	team := fs.String("team", "", "team label isolating this pair in a shared data dir (F-5); usually unneeded with auto-scope")
	forceNew := fs.Bool("force-new", false, "register a brand-new session even if a matching identity exists (skips resume)")
	noListen := fs.Bool("no-listen", false, "for role=esc, register only and do NOT enter listen (default: esc listens)")
	untilDeadline := fs.String("until-deadline", "", "for the esc listen handoff: window as a Go duration (e.g. 2h, 30m); see listen --until-deadline")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *role == "" {
		return fmt.Errorf("bootstrap: --role is required (val|esc) — it is the one thing an agent must know about itself")
	}
	if *team != "" {
		if err := security.ValidateTeamID(*team); err != nil {
			return err
		}
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	// Sweep dead orphans first, exactly as register does — a bootstrap is a
	// register superset, so it inherits the same hygiene (F-10). Logged to stderr.
	runAutoGC(cfg, os.Stderr)

	pp := *projectPath
	if pp == "" {
		pp, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("bootstrap: getwd: %w", err)
		}
	}
	scope := resolveScope(pp)

	// Discovery: peers in MY scope (scope-filtered on the project root, not the
	// cwd, so a val at the repo root and an esc in docs/ see each other). Include
	// stale on purpose: a just-registered val that is not yet orchestrating nor in
	// listen goes stale after StaleSeconds — excluding it would miss the naming
	// anchor. Better to see it stale than to miss it (we flag a stale pick below).
	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, true, *team, scope)
	if err != nil {
		return fmt.Errorf("bootstrap: discover peers: %w", err)
	}
	peer, hasPeer := selectPeer(*role, peers)

	// Adaptive name.
	var name, basis string
	if *agentNameOverride != "" {
		name, basis = *agentNameOverride, "override"
	} else {
		var peerForNaming []peerSummary
		if hasPeer {
			peerForNaming = []peerSummary{*peer}
		}
		name, basis = deriveAgentName(*role, filepath.Base(scope), peerForNaming)
	}
	if hasPeer && peer.Stale {
		fmt.Fprintf(os.Stderr, "cab-bridge: bootstrap: paired peer %s (%s) is stale — using it for naming anyway (a val outside listen is normally stale)\n",
			peer.SessionID, peer.AgentName)
	}

	// Idempotent register (F-27 reconnect-or-register). StartedAt is the ground
	// truth of what Register did: a fresh register stamps it to now, a resume
	// preserves the original — so a StartedAt earlier than this call means resume.
	beforeRegister := time.Now().UTC()
	mf, release, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: pp,
		AgentName:   name,
		Role:        *role,
		ForceNew:    *forceNew,
		TeamID:      *team,
		Scope:       scope,
		Resume:      !*forceNew, // bootstrap is idempotent by default; --force-new opts out
	})
	if err != nil {
		return err
	}
	_ = release() // same as register: release immediately so a later listen re-acquires

	action := "registered-new"
	if mf.StartedAt.Before(beforeRegister) {
		action = "resumed"
	}

	report := bootstrapReport{
		SessionID:   mf.SessionID,
		AgentName:   mf.AgentName,
		Role:        mf.Role,
		Scope:       mf.Scope,
		Action:      action,
		NamingBasis: basis,
	}
	if hasPeer {
		report.Peer = &bootstrapPeerInfo{
			SessionID: peer.SessionID,
			AgentName: peer.AgentName,
			Role:      peer.Role,
			Stale:     peer.Stale,
		}
	}

	// role=val: make the orchestrator observable (F-23a) and exit — a val
	// orchestrates, it does not listen. The state set is best-effort-fatal: if it
	// fails the val would falsely appear stale, so surface it.
	if *role == "val" {
		if err := mgr.SetState(mf.SessionID, session.StateOrchestrating); err != nil {
			return fmt.Errorf("bootstrap: set orchestrating state: %w", err)
		}
		report.State = session.StateOrchestrating
		report.Listening = false
		return printBootstrapReport(os.Stdout, report)
	}

	// role=esc with --no-listen, or any non-listening role: register-only summary.
	if *role != "esc" || *noListen {
		report.Listening = false
		return printBootstrapReport(os.Stdout, report)
	}

	// role=esc default: hand off to listen --wait-one (native wake). The summary
	// goes to STDERR so stdout carries only the listen NDJSON payload, exactly as
	// a bare `listen` would — a run-in-background caller reads messages, not our
	// envelope. We reuse runListen by args (VAL-ratified: zero regression risk;
	// runListen is already tested) rather than extracting the loop.
	report.Listening = true
	if err := printBootstrapReport(os.Stderr, report); err != nil {
		return err
	}
	listenArgs := []string{"--wait-one", "--session-id", mf.SessionID}
	if *untilDeadline != "" {
		listenArgs = append(listenArgs, "--until-deadline", *untilDeadline)
	}
	return runListen(listenArgs)
}

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

// printBootstrapReport writes the JSON summary to w (stdout for non-listening
// bootstraps, stderr for the esc-listen handoff).
func printBootstrapReport(w *os.File, r bootstrapReport) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("bootstrap: marshal report: %w", err)
	}
	fmt.Fprintln(w, string(out))
	return nil
}
