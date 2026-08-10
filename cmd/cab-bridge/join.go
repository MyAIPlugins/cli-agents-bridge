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
	role := fs.String("role", "", "this agent's role (required): "+session.RoleNamesWithNote())
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

	pp := *projectPath
	if pp == "" {
		if pp, err = os.Getwd(); err != nil {
			return fmt.Errorf("join: getwd: %w", err)
		}
	}
	scope := resolveScope(pp)

	// AFTER the scope is known, never before: the sweep is confined to this
	// caller's project root, and running it earlier would have had nothing to
	// confine it with.
	runAutoGC(cfg, scope, os.Stderr)

	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, cfg.MaxMessageBytes, true, *team, scope)
	if err != nil {
		return fmt.Errorf("join: discover who is here: %w", err)
	}

	// Who already works here? This directory holds at most ONE session — that is
	// the invariant, and F-110 is what it costs when it is only half applied — so
	// an existing one is not "another agent": it is this working place, whatever
	// name and whatever role it answers to.
	occupant, occupied := findSessionHere(mgr, peers, pp)

	// F-116: a name somebody TYPED is refused if it carries the separator; the
	// deeper guard lives in Register/RenameAgent, this one exists only so the
	// error names the flag the caller used instead of arriving as "register:".
	if err := session.ValidateAgentName(*agentName); err != nil {
		return fmt.Errorf("join: --agent-name: %w", err)
	}

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
		// A DERIVED name is sanitised, not refused: nobody typed the directory,
		// and failing here would be an error for a choice the caller never made.
		// Said out loud, because a silent rename of one's own identity is exactly
		// the kind of quiet substitution this project keeps removing.
		base, changed := session.SanitizeDerivedName(filepath.Base(pp))
		if changed {
			fmt.Fprintf(os.Stderr, "join: this directory's name contains %q, which cannot appear in an agent name (it separates a name from its project when addressing across repositories) — deriving from %q instead\n",
				session.ScopeSeparator, base)
		}
		name, _ = deriveAgentName(*role, base, peers)
	}

	// CROSS-SCOPE GUARD, and it is not about ambiguity — scopes already isolate,
	// so two `CRI-payload` in two repositories route perfectly well. It is a guard
	// against the human error, and the error is real: a critic was started in this
	// repository and told "you are CRI-payload" out of muscle memory, the tool
	// allowed it, and two agents ended up with one name in two projects. Nobody
	// noticed until a table was read.
	//
	// It looks at EVERY scope on purpose. The existing guard below is fed by
	// collectPeers, which filters by scope — which is why today's behaviour is
	// exactly inverted: it blocks inside the repo, where a second agent asking for
	// a taken name is its replacement, and allows across repos, where it is a
	// mistake. This half fixes the second.
	//
	// CONSEQUENCE, stated because the brief does not: agent names become unique
	// per DATA DIR, not per scope. Somebody working on three projects can no
	// longer have a `VAL-x` in each. That is the price of the guard, and it is
	// deliberate — but it is a price.
	if !*forceNew {
		everywhere, _, aerr := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, cfg.MaxMessageBytes, true, "", "")
		if aerr != nil {
			return fmt.Errorf("join: check the name across projects: %w", aerr)
		}
		if other, otherPath, otherScope, clash := findNameInAnotherScope(mgr, everywhere, name, scope); clash {
			wayOut := ""
			if other.Stale {
				wayOut = fmt.Sprintf("\n  That session shows no sign of life (last seen %s ago). If it is genuinely\n"+
					"  finished, remove it and the name frees up:\n"+
					"    cab-bridge cleanup --scope=my-session --session-id=%s",
					time.Since(other.LastHeartbeat).Truncate(time.Second), other.SessionID)
			}
			return fmt.Errorf("join: the name %q is already used by %s in ANOTHER project (%s).\n"+
				"  Names must be unique across every project sharing this data dir, so that a name\n"+
				"  always identifies one agent — this is the guard against giving two agents one\n"+
				"  name out of habit.\n"+
				"  That session lives in %s; this one is in %s.\n"+
				"  The tool cannot tell which of the two is the mistake: pick a different name for\n"+
				"  whichever is wrong.\n"+
				"    cab-bridge join --role=%s --agent-name=<name>%s",
				name, other.SessionID, otherScope, otherPath, scope, *role, wayOut)
		}
	}

	if !*forceNew {
		// Inside MY OWN scope, another directory already answers to this name. The
		// invariant is that no two sessions in one scope share a name, so one of
		// them has to give — and which one depends on whether anybody is home.
		//
		// LIVE: refuse. Taking the name from a working agent would strip its
		// identity and quietly reroute to me the messages its peers address by
		// name, with neither of us noticing. Between stopping the arriver and
		// orphaning the worker, stop the arriver.
		//
		// STALE: take it over. That is the real case, twice in one evening: an
		// agent restarted and reclaiming its place. Nobody loses anything, because
		// there is nobody on the other side.
		if other, otherPath, clash := findNameElsewhere(mgr, peers, pp, name); clash {
			// The FULL path, not the basename: an error that names a place must
			// name one the reader can go to.
			return fmt.Errorf("join: this project already has a LIVE %q (%s, in %s, last seen %s ago).\n"+
				"  Two sessions of one name in one project would make every by-name recipient\n"+
				"  ambiguous, so this one stops rather than take the name from an agent at work.\n"+
				"  Pick another name:  cab-bridge join --role=%s --agent-name=<name>\n"+
				"  — or stop that session first, if it is the one you are replacing.",
				name, other.SessionID, otherPath, time.Since(other.LastHeartbeat).Truncate(time.Second), *role)
		}
		// The stale twin: it yields the name and keeps everything else. Renaming it
		// rather than deleting it means its mailbox stays recoverable and the
		// auto-gc collects it in its own time — and `formerAgentNames` records
		// where the name went, so this stays inspectable afterwards.
		if prev, prevPath, found := findStaleNamesake(mgr, peers, pp, name); found {
			retired := name + "-superseded-" + prev.SessionID[:4]
			if err := mgr.RenameAgent(prev.SessionID, retired); err != nil {
				return fmt.Errorf("join: hand the name %q over from %s: %w", name, prev.SessionID, err)
			}
			fmt.Fprintf(os.Stderr, "join: %q was held by a stale session (%s in %s); it is now %q and this session takes the name\n",
				name, prev.SessionID, prevPath, retired)
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
		// The same move for the ROLE, and for the same reason (F-110): reuse
		// matches on it too, so leaving the old one on disk sends this join into a
		// fresh registration and leaves two live sessions of one name on one path.
		// Verified on the real binary — fixing the lookup alone was not enough.
		if occupied && occupant.Role != *role {
			if err := mgr.SetRole(occupant.SessionID, *role); err != nil {
				return fmt.Errorf("join: set role of %s to %q: %w", occupant.SessionID, *role, err)
			}
			fmt.Fprintf(os.Stderr, "join: %s is now role %q (was %q) — same session, same inbox\n",
				occupant.SessionID, *role, occupant.Role)
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
		Scope:     effectiveScope(mf),
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
	entries, _, _, err := readMailbox(filepath.Join(cfg.DataDir, "sessions", sid, "inbox"), cfg.MaxMessageBytes)
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

// findSessionHere reports the session already registered on this exact project
// path — the one working place this directory holds, under whatever name and
// whatever ROLE it currently answers to.
//
// It used to be findNameClash, which took the wanted name and reported a
// "clash" whenever the existing name differed. That framing was the error: two
// sessions on one path are refused precisely so that the one living here IS the
// caller. The name it carries is a fact about it, not a rival claim.
//
// The ROLE used to be part of the match, and that was F-110: restarting an agent
// with a different role found this place EMPTY, so join registered a SECOND
// session on the same path under the same name — two live homonyms in one scope,
// exactly what the naming rule forbids. Reproduced in two commands. The role is
// an attribute of the session, like the name that RenameAgent already updates;
// it is not what identifies the place. The place is the path.
//
// None of the three name guards could have caught it: findNameElsewhere only
// looks at OTHER paths, findStaleNamesake skips its own directory by design, and
// findNameInAnotherScope skips its own scope. All three delegate "same place" to
// this function, which is why the hole was here and not there.
func findSessionHere(mgr *session.Manager, peers []peerSummary, projectPath string) (peerSummary, bool) {
	var best peerSummary
	found := false
	for _, p := range peers {
		mf, err := mgr.LoadManifest(p.SessionID)
		if err != nil {
			continue
		}
		if filepath.Clean(mf.ProjectPath) != filepath.Clean(projectPath) {
			continue
		}
		if !found || betterOccupant(p, best) {
			best, found = p, true
		}
	}
	return best, found
}

// betterOccupant ranks two sessions found on ONE path: alive beats stale, then
// the most recent heartbeat, then the id so the choice is deterministic.
//
// It matters on a data dir that ALREADY holds two homonyms — the state this fix
// prevents but does not repair. collectPeers returns them in os.ReadDir order,
// so without a rank the join could adopt the DEAD twin and leave the live one
// standing: the defect reproduced by its own fix.
func betterOccupant(candidate, current peerSummary) bool {
	if candidate.Stale != current.Stale {
		return !candidate.Stale
	}
	if !candidate.LastHeartbeat.Equal(current.LastHeartbeat) {
		return candidate.LastHeartbeat.After(current.LastHeartbeat)
	}
	return candidate.SessionID < current.SessionID
}

// findStaleNamesake reports a session in MY scope, in another directory, holding
// this name and showing no sign of life. It is the complement of
// findNameElsewhere, which reports only the live ones: together they cover the
// same shape, and the pair is what makes the live/stale decision explicit rather
// than accidental.
// Takes no role either, for the same reason as findNameElsewhere: the parameter
// was declared and never read.
func findStaleNamesake(mgr *session.Manager, peers []peerSummary, projectPath, wantName string) (peerSummary, string, bool) {
	for _, p := range peers {
		if p.AgentName != wantName || !p.Stale {
			continue
		}
		mf, err := mgr.LoadManifest(p.SessionID)
		if err != nil {
			continue
		}
		if filepath.Clean(mf.ProjectPath) == filepath.Clean(projectPath) {
			continue // my own directory: that is the rename case, not a takeover
		}
		return p, mf.ProjectPath, true
	}
	return peerSummary{}, "", false
}

// findNameInAnotherScope reports a session using this name in a DIFFERENT scope,
// i.e. in another repository sharing the data dir. Live ones only: a dead
// session's name is nobody's, and refusing on its behalf would strand an agent
// whose predecessor simply died — the same rule findNameElsewhere applies.
//
// Returns the occupant, its project path and its scope, because an error about a
// name collision across projects has to name BOTH places or the reader cannot
// tell which one they are in.
func findNameInAnotherScope(mgr *session.Manager, peers []peerSummary, wantName, myScope string) (peerSummary, string, string, bool) {
	for _, p := range peers {
		if p.AgentName != wantName {
			continue
		}
		mf, err := mgr.LoadManifest(p.SessionID)
		if err != nil {
			continue
		}
		// Effective on both sides, and the same conflation bit BOTH ways: two
		// legacy sessions read as one project (so a homonym in another repo did
		// not block), and a legacy against a current one in the SAME repo read as
		// two (so a legitimate name was refused).
		if sameProject(effectiveScope(mf), myScope) {
			continue // same project: that is the other guard's business
		}
		// Stale ones block TOO — the name belongs to another project, and taking
		// it silently is precisely the mistake the rule prevents. But the error
		// then has to say the session is dead and how to remove it, or a session
		// abandoned months ago in a repository you never touch would hold a name
		// hostage forever with no way to find out why.
		return p, mf.ProjectPath, effectiveScope(mf), true
	}
	return peerSummary{}, "", "", false
}

// findNameElsewhere reports a LIVE session already using this name from a
// different project path. Stale ones do not count: a dead session's name is
// free, and refusing on its behalf would strand an agent whose predecessor
// simply died.
// It takes no role, and never did in substance: the parameter it used to
// declare was never read in the body. A signature that promises a behaviour the
// body does not have is worse than an optimistic comment — the compiler does not
// object and the reader has no reason to doubt it. It sent the F-110 diagnosis
// to the wrong function, twice, before anybody opened the body.
func findNameElsewhere(mgr *session.Manager, peers []peerSummary, projectPath, wantName string) (peerSummary, string, bool) {
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
