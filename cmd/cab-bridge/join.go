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

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
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
//
//   - It never leaves two sessions on one project path (F-90). `register
//     --resume` matches identity on the agent name, so a different name fell
//     through to a fresh register and produced exactly that — a hard ambiguity
//     that blocks every id-free command afterwards.
//
//     The first cure was to stop and ask. It was the wrong shape: three fresh
//     agents met the stop, read it, and destroyed their sessions with
//     `cleanup && join` to get the name their human had given them, because
//     neither offered road was theirs. join now RENAMES in place instead — same
//     id, same mailbox — and only stops for the ambiguity that is real: a name
//     already live in another directory.
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
	role := fs.String("role", "", "this agent's role (required): "+session.RoleNames())
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
		// architect belongs here: it is a real routing role (val <-> architect is
		// allowed), and leaving it out made a fresh reviewer pick `esc` and walk
		// into the esc->esc wall before discovering the role meant for them.
		// One source for the list (session.SelectableRoles). Written out by hand
		// here, this block advertised `architect` — reserved for Claude Desktop —
		// and omitted `critic`, so a fresh reviewer was told to take a role that
		// was not its own. Twice in two days a hand-kept list sent someone to the
		// wrong place; a list that cannot disagree with itself cannot do that.
		return errors.New("join: --role is required — it is the one thing an agent must know about itself:\n" +
			session.RoleLines())
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

	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, cfg.MaxMessageBytes, true, *team, scope)
	if err != nil {
		return fmt.Errorf("join: discover who is here: %w", err)
	}

	// Who already works here? This directory holds at most one session per role
	// (that is the invariant F-90 protects), so an existing one is not "another
	// agent" — it is this working place, whatever name it answers to.
	occupant, occupied := findSessionHere(mgr, peers, *role, pp)

	name := *agentName
	switch {
	case name != "":
		// An explicit name is an INSTRUCTION, not a preference. Three agents were
		// told their names by a human, the skill said the name derives itself, and
		// all three ended up as something else — then destroyed their sessions with
		// `cleanup && join` to fix it, inventing the one manoeuvre v0.8 exists to
		// make unnecessary. What was missing was not a clearer error: it was the
		// third road, the obvious one, which is to just change the name.
	case occupied && !*forceNew:
		// Nothing was asked for and a session is already here: this is a RETURN,
		// not a first arrival. Take the name it already has.
		//
		// Deriving one here was the bug underneath the reported one: derivation
		// invents a name for a session that does not exist yet, so applying it to
		// a session that DOES produces a phantom, compares it with the real name,
		// and calls the difference a collision. An agent named by a human then met
		// that stop on every single re-arm, forever — not once at onboarding.
		name = occupant.AgentName
	default:
		// Genuinely new here: invent one. From the WORKING DIRECTORY, not the scope
		// (CRI2 P0) — deriving from the scope is not injective, so every agent of a
		// role in one repository would land on the same name.
		name, _ = deriveAgentName(*role, filepath.Base(pp), peers)
	}

	if !*forceNew {
		// The one ambiguity that is real, and the only surviving stop: this name is
		// LIVE somewhere else. Renaming into it, or registering under it, would make
		// every by-name recipient ambiguous — and unlike the case below, the other
		// session is genuinely another agent, in another directory.
		//
		// Checked BEFORE the rename, so a rename can never take a name that is in use.
		if other, otherPath, clash := findNameElsewhere(mgr, peers, *role, pp, name); clash {
			// The FULL path, not the basename: an error that names a place must
			// name one the reader can go to. ProjectName is filepath.Base, so
			// "run this from cridir" pointed at something that is not a
			// directory — and a repo can hold several with that name.
			return fmt.Errorf("join: the name %q is already taken by %s, which lives in %s — not this directory.\n"+
				"  Two sessions with one name would make every by-name recipient ambiguous.\n"+
				"  Either pick your own name:  cab-bridge join --role=%s --agent-name=<name>\n"+
				"  or run this from %s if you ARE that agent coming back",
				name, other.SessionID, otherPath, *role, otherPath)
		}
		// RENAME instead of stopping. Same id, same mailbox, same cursor: the name
		// is a lookup label, and identity is the id — every message on disk already
		// carries it, so nothing in flight and no open ask depends on this string.
		//
		// It must happen BEFORE Register, because reuse matches on the agent name
		// (reconnect.go findIdentityMatches): with the old name still on disk, a
		// resume finds nothing and falls through to creating a SECOND session on
		// this path — the exact accident the stop was there to prevent.
		if occupied && occupant.AgentName != name {
			if err := mgr.RenameAgent(occupant.SessionID, name); err != nil {
				return fmt.Errorf("join: rename %s to %q: %w", occupant.SessionID, name, err)
			}
			fmt.Fprintf(os.Stderr, "join: %s is now %q (was %q) — same session, same inbox\n",
				occupant.SessionID, name, occupant.AgentName)
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

	// Redelivery across lives (DESIGN §2.3, CRI2 P1-4): an ask that was already
	// handed to a `next` in a previous incarnation goes back to UNREAD, so this
	// life sees it. Without this the cursor only grows — a page lost to a compact
	// left its asks NOTIFIED forever, invisible to every later `next`, and the
	// next reply closed them without anyone having read them.
	//
	// ASKS ONLY: `tell` and `response` are one-shot per cursor, and re-waking a
	// notification from three days ago is precisely the indiscriminate reset the
	// design rules out.
	replayed, rerr := replayOpenAsks(mgr, cfg, mf.SessionID)
	if rerr != nil {
		return fmt.Errorf("join: replay open asks: %w", rerr)
	}
	if replayed > 0 {
		fmt.Fprintf(os.Stderr, "join: %d open ask(s) from a previous session will be delivered again by next\n", replayed)
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

// replayOpenAsks moves every still-open ask back to UNREAD and reports how many.
func replayOpenAsks(mgr *session.Manager, cfg config.Config, sid string) (int, error) {
	cursor, _, err := mgr.ReadWakeCursor(sid)
	if err != nil {
		return 0, err
	}
	entries, _, err := readMailbox(filepath.Join(cfg.DataDir, "sessions", sid, "inbox"), cfg.MaxMessageBytes)
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, e := range entries {
		if e.msg.Type == message.TypeQuery && cursor.IsNotified(e.msg.ID) {
			ids = append(ids, e.msg.ID)
		}
	}
	if err := mgr.ForgetNotified(sid, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// findSessionHere reports the session of this role already registered on this
// exact project path — the one working place this directory holds, under
// whatever name it currently answers to.
//
// It used to be findNameClash, which took the wanted name and reported a
// "clash" whenever the existing name differed. That framing was the error: two
// sessions on one path are refused precisely so that the one living here IS the
// caller. The name it carries is a fact about it, not a rival claim.
func findSessionHere(mgr *session.Manager, peers []peerSummary, role, projectPath string) (peerSummary, bool) {
	for _, p := range peers {
		if p.Role != role {
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

// findNameElsewhere reports a LIVE session already using this name from a
// different project path. Stale ones do not count: a dead session's name is
// free, and refusing on its behalf would strand an agent whose predecessor
// simply died.
func findNameElsewhere(mgr *session.Manager, peers []peerSummary, role, projectPath, wantName string) (peerSummary, string, bool) {
	for _, p := range peers {
		if p.AgentName != wantName || p.Stale {
			continue
		}
		mf, err := mgr.LoadManifest(p.SessionID)
		if err != nil {
			continue
		}
		if filepath.Clean(mf.ProjectPath) != filepath.Clean(projectPath) {
			// Return the path too: the caller needs somewhere the reader can
			// actually go, and it is already loaded here.
			return p, mf.ProjectPath, true
		}
	}
	return peerSummary{}, "", false
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
