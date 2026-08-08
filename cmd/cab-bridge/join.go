package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// join is the one command an agent runs at the start (DESIGN v0.8 §2.2),
// replacing register/bootstrap/whoami/overview as the way in.
//
// Two things distinguish it from what it replaces:
//
//   - It prints WHO IS HERE — every live agent in the scope, not "the peer".
//     bootstrap picked a single pairing counterpart, which is right for a pair
//     and wrong for the group that is now the normal setup: with three peers
//     alive, "peer: none" is what sent CRI into passive waiting (F-92). A list
//     cannot be wrong in that way.
//   - On a name mismatch it STOPS AND ASKS instead of registering a second
//     session (F-90): `register --resume` with a different --agent-name matches
//     identity strictly, falls through to a fresh register, and leaves two
//     sessions on one projectPath — a hard ambiguity that blocks every id-free
//     command afterwards. Reproduced in thirty seconds; the recovery is far more
//     expensive than the question.
type joinReport struct {
	SessionID string     `json:"sessionId"`
	AgentName string     `json:"agentName"`
	Role      string     `json:"role"`
	Scope     string     `json:"scope,omitempty"`
	Action    string     `json:"action"` // "resumed" | "registered-new"
	Here      []joinPeer `json:"here"`   // everyone else in this scope, live first
	Hint      string     `json:"hint"`
}

type joinPeer struct {
	SessionID string `json:"sessionId"`
	AgentName string `json:"agentName"`
	Role      string `json:"role"`
	State     string `json:"state,omitempty"`
	Stale     bool   `json:"stale"`
}

func runJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	role := fs.String("role", "", "this agent's role (required): val|esc|observer")
	agentName := fs.String("agent-name", "", "this agent's name; empty = derived from the scope")
	projectPath := fs.String("project-path", "", "project root (default: cwd) — test injection point")
	team := fs.String("team", "", "team label isolating this group in a shared data dir; usually unneeded")
	forceNew := fs.Bool("force-new", false, "register a second session even if one already matches this identity")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *role == "" {
		return errors.New("join: --role is required (val|esc|observer) — it is the one thing an agent must know about itself")
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
	runAutoGC(cfg, os.Stderr)

	pp := *projectPath
	if pp == "" {
		if pp, err = os.Getwd(); err != nil {
			return fmt.Errorf("join: getwd: %w", err)
		}
	}
	scope := resolveScope(pp)

	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, true, *team, scope)
	if err != nil {
		return fmt.Errorf("join: discover who is here: %w", err)
	}

	name := *agentName
	if name == "" {
		name, _ = deriveAgentName(*role, filepath.Base(scope), peers)
	}

	// F-90 stop-and-ask, BEFORE registering anything.
	if !*forceNew {
		if occupant, clash := findNameClash(mgr, peers, *role, pp, name); clash {
			return fmt.Errorf("join: this working directory already has a %s session named %q (%s), and you asked to join as %q.\n"+
				"  Registering both would leave two sessions on one project path, which blocks every command that resolves by directory.\n"+
				"  Either join with the existing name:   cab-bridge join --role=%s --agent-name=%s\n"+
				"  or, if you really want a second one:  cab-bridge join --role=%s --agent-name=%s --force-new",
				*role, occupant.AgentName, occupant.SessionID, name, *role, occupant.AgentName, *role, name)
		}
	}

	before := time.Now().UTC()
	mf, release, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: pp,
		AgentName:   name,
		Role:        *role,
		ForceNew:    *forceNew,
		TeamID:      *team,
		Scope:       scope,
		Resume:      !*forceNew,
	})
	if err != nil {
		return err
	}
	_ = release()

	action := "registered-new"
	if mf.StartedAt.Before(before) {
		action = "resumed"
	}
	if *role == session.RoleVal {
		// An orchestrator does not wait, so without this it would look stale to
		// everyone else within StaleSeconds (F-23a).
		if err := mgr.SetState(mf.SessionID, session.StateOrchestrating); err != nil {
			return fmt.Errorf("join: set orchestrating state: %w", err)
		}
	}

	report := joinReport{
		SessionID: mf.SessionID,
		AgentName: mf.AgentName,
		Role:      mf.Role,
		Scope:     mf.Scope,
		Action:    action,
		Here:      othersHere(peers, mf.SessionID),
		Hint:      "run next to receive work",
	}
	return printJoinReport(os.Stdout, report)
}

// findNameClash reports an existing session with the same (role, scope,
// projectPath) but a DIFFERENT agent name — the shape that would otherwise
// become a second session on one project path.
func findNameClash(mgr *session.Manager, peers []peerSummary, role, projectPath, wantName string) (peerSummary, bool) {
	for _, p := range peers {
		if p.Role != role || p.AgentName == wantName {
			continue
		}
		mf, err := mgr.LoadManifest(p.SessionID)
		if err != nil {
			continue
		}
		if filepath.Clean(mf.ProjectPath) == filepath.Clean(projectPath) {
			return p, true
		}
	}
	return peerSummary{}, false
}

// othersHere lists everyone except me, live first then stale, each group by
// name — a deterministic order, and the useful ones on top.
func othersHere(peers []peerSummary, meID string) []joinPeer {
	out := []joinPeer{}
	for _, p := range peers {
		if p.SessionID == meID {
			continue
		}
		out = append(out, joinPeer{
			SessionID: p.SessionID,
			AgentName: p.AgentName,
			Role:      p.Role,
			State:     p.State,
			Stale:     p.Stale,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stale != out[j].Stale {
			return !out[i].Stale
		}
		return out[i].AgentName < out[j].AgentName
	})
	return out
}

func printJoinReport(w io.Writer, r joinReport) error {
	if _, err := fmt.Fprintf(w, "you are %s (%s), role %s — %s\n", r.AgentName, r.SessionID, r.Role, r.Action); err != nil {
		return err
	}
	if len(r.Here) == 0 {
		if _, err := fmt.Fprintln(w, "nobody else is here yet"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, "here with you (%d):\n", len(r.Here)); err != nil {
			return err
		}
		for _, p := range r.Here {
			mark := ""
			if p.Stale {
				mark = "  (stale)"
			}
			if _, err := fmt.Fprintf(w, "  %-16s %s  %s%s\n", p.AgentName, p.SessionID, p.Role, mark); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "%s\n", r.Hint)
	return err
}
