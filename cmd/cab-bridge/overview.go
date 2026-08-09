package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// overviewReport is the F-42 at-a-glance view of a session's world in ONE call:
// who I am, my paired peer (the complementary role in my scope), and my unread
// inbox — resolved id-free from the cwd by default, or from an explicit
// --session-id (A-3/F-86, for a worktree or shared scope where the cwd lookup
// would resolve the wrong session). Worktree-aware by construction: "me" is
// resolved through resolveCurrentSession (the B-1 scope guardrail), the peer
// from the shared scope (F-41 makes a VAL at the main repo and an ESC in a
// worktree of the same repo share that scope). It reuses the existing building
// blocks (collectPeers/selectPeer, collectInbox) rather than duplicating them.
type overviewReport struct {
	Me overviewSelf `json:"me"`

	// F-81: listener observability — whether THIS session has a live waiter, which
	// PID holds it, and since when. Answers the CRI ask "am I really listening?"
	// that PID/heartbeat/state alone could not. All three come from the ownership
	// record (ListenerOwner.Listening); pid/since are only meaningful, and only
	// emitted, when active.
	//
	// There is no listenerUntil any more: a wait has no deadline (§2.2 rev.
	// cdb21dc), so the field's writer went away with `listen` and it kept being
	// published from whatever the manifest still held — an expiry from a world
	// that no longer exists, presented as current.
	ListenerActive bool       `json:"listenerActive"`
	ListenerPid    int        `json:"listenerPid,omitempty"`
	ListenerSince  *time.Time `json:"listenerSince,omitempty"`

	// B-2 listener ownership, from listener.json — distinct from the F-81 active
	// signal above (which is manifest PID + window). Generation is the monotone
	// claim counter; ReclaimPending is true when a reclaim revoked the listener
	// and no new listen has claimed yet (listener.json PID == 0).
	ListenerGeneration     int  `json:"listenerGeneration,omitempty"`
	ListenerReclaimPending bool `json:"listenerReclaimPending,omitempty"`

	Peer *overviewPeer `json:"peer"` // null when no complementary peer is registered yet
	// Inbox holds the UNREAD messages only: what `next` would still deliver. Not
	// the contents of inbox/, which under the mailbox model also holds everything
	// already read and not yet archived.
	Inbox []overviewMsg `json:"inbox"`
}

type overviewSelf struct {
	SessionID string `json:"sessionId"`
	AgentName string `json:"agentName"`
	Role      string `json:"role"`
	Scope     string `json:"scope,omitempty"`
	State     string `json:"state,omitempty"`
	Stale     bool   `json:"stale"`
}

type overviewPeer struct {
	SessionID string `json:"sessionId"`
	AgentName string `json:"agentName"`
	Role      string `json:"role"`
	State     string `json:"state,omitempty"`
	Stale     bool   `json:"stale"`
}

type overviewMsg struct {
	MsgID         string `json:"msgId"`
	From          string `json:"from"`          // sender session id
	FromAgentName string `json:"fromAgentName"` // sender agent name when known
	Type          string `json:"type"`
}

func runOverview(args []string) error {
	fs := flag.NewFlagSet("overview", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON on stdout (default: human-readable)")
	// A-3 (F-86): overview defaults to an id-free cwd lookup (F-42), but in a
	// worktree or a shared scope the cwd lookup resolves the WRONG session (e.g.
	// it sees an ESC worktree as the VAL), making the overview useless exactly
	// where it is needed. An explicit --session-id wins when passed; the default
	// stays id-free.
	sessionIDFlag := fs.String("session-id", "", "session to report on (default: id-free cwd lookup, F-42); pass it in a worktree or shared scope where the cwd lookup would resolve the wrong session")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	// Resolve "me" through the shared B-1 guardrail: an explicit --session-id
	// wins (worktree / shared scope, F-86); otherwise the id-free cwd lookup
	// (F-42 default) with the scope-collision guardrail (hard-ambiguity reject,
	// shared-scope warning on stderr — never polluting the --json stdout below).
	sid, err := resolveCurrentSession(mgr, "overview", *sessionIDFlag)
	if err != nil {
		return err
	}

	report, err := buildOverview(mgr, cfg, sid)
	if err != nil {
		return err
	}

	if *asJSON {
		out, merr := json.MarshalIndent(report, "", "  ")
		if merr != nil {
			return fmt.Errorf("overview: marshal: %w", merr)
		}
		fmt.Println(string(out))
		return nil
	}
	printOverviewHuman(os.Stdout, report)
	return nil
}

// buildOverview assembles the report for an already-resolved session id. Split
// from runOverview so it is table-testable with planted manifests (no cwd
// dance). All three lookups are pure reads — overview never consumes a message
// or mutates a manifest.
func buildOverview(mgr *session.Manager, cfg config.Config, sid string) (overviewReport, error) {
	me, err := mgr.LoadManifest(sid)
	if err != nil {
		return overviewReport{}, fmt.Errorf("overview: load own manifest: %w", err)
	}
	now := time.Now().UTC()

	report := overviewReport{
		Me: overviewSelf{
			SessionID: me.SessionID,
			AgentName: me.AgentName,
			Role:      me.Role,
			Scope:     me.Scope,
			State:     me.State,
			Stale:     session.IsStale(me, cfg.StaleSeconds, now),
		},
		Inbox: []overviewMsg{},
	}

	// Am I actively listening — and who owns that wait? ONE read of the ownership
	// record answers both, because both are the same fact (ListenerOwner.Listening).
	//
	// It used to be two: the manifest's waitingSince marker for "active", this
	// record for generation/reclaim. That split is what let an evicted `next`
	// clear the marker of the live one that had replaced it, so overview told a
	// listening session it was not listening — on the exact command an agent uses
	// to decide whether to re-arm, which made the answer destroy the thing it was
	// asked about. The PID reported here is now the OWNER's, so the `kill` line
	// below points at the process that actually holds the wait.
	//
	// Best-effort: a missing/unreadable record leaves the fields zero (a session
	// that never listened), never an error on a pure observability path.
	if owner, ok, oerr := mgr.ReadListener(sid); oerr == nil && ok {
		report.ListenerGeneration = owner.Generation
		report.ListenerReclaimPending = owner.PID == 0
		if owner.Listening() {
			report.ListenerActive = true
			report.ListenerPid = owner.PID
			claimedAt := owner.ClaimedAt
			report.ListenerSince = &claimedAt
		}
	}

	// Peer: the complementary role within my own scope+team. collectPeers
	// includes me, but selectPeer never picks my own role, so I am not selected.
	// Filtering on MY stored scope (not resolveScope(cwd)) keeps this correct even
	// for an inherited/legacy scope, though F-41 makes them equal for a worktree.
	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, cfg.MaxMessageBytes, true, me.TeamID, me.Scope)
	if err != nil {
		return overviewReport{}, fmt.Errorf("overview: discover peers: %w", err)
	}
	if peer, ok := selectPeer(me.Role, peers); ok {
		report.Peer = &overviewPeer{
			SessionID: peer.SessionID,
			AgentName: peer.AgentName,
			Role:      peer.Role,
			State:     peer.State,
			Stale:     peer.Stale,
		}
	}

	// The UNREAD inbox — what `next` would still hand over — not every file in
	// inbox/. Under the mailbox model a message stays put after being delivered
	// (only `reply` archives), so listing the directory listed mail this agent had
	// already read and answered, and called it "pending". Being told you have two
	// messages waiting when you have none is not a rounding error on the command
	// you run to orient yourself.
	//
	// `inbox --list` remains the full view, processed/ included: this is the
	// glance, that is the inspection.
	cursor, _, err := mgr.ReadWakeCursor(sid)
	if err != nil {
		return overviewReport{}, fmt.Errorf("overview: read wake cursor: %w", err)
	}
	entries, err := collectInbox(filepath.Join(cfg.DataDir, "sessions", sid), cfg.MaxMessageBytes)
	if err != nil {
		return overviewReport{}, fmt.Errorf("overview: read inbox: %w", err)
	}
	for _, e := range entries {
		// type=ack is excluded for the same reason `next` excludes it: a delivery
		// receipt is not work waiting, and counting it promises a message that
		// will never be handed over.
		if e.Box != "inbox" || e.Type == message.TypeAck || cursor.IsNotified(e.MsgID) {
			continue
		}
		report.Inbox = append(report.Inbox, overviewMsg{
			MsgID:         e.MsgID,
			From:          e.From,
			FromAgentName: e.FromAgentName,
			Type:          e.Type,
		})
	}
	return report, nil
}

// printOverviewHuman renders the report as three scannable lines (me / peer /
// inbox). English, consistent with every other cab-bridge command's output.
func printOverviewHuman(w io.Writer, r overviewReport) {
	fmt.Fprintf(w, "me:    %s  (%s)  role %s  scope %s  state %s%s\n",
		r.Me.AgentName, r.Me.SessionID, r.Me.Role, overviewDash(r.Me.Scope), overviewState(r.Me.State), overviewLive(r.Me.Stale))

	// F-81 listener line. English, consistent with the rest of this output. The
	// remaining window is computed at display time (now-relative), truncated to
	// the second.
	switch {
	case r.ListenerActive:
		// No expiry to print: the wait has no window (§2.2 rev. cdb21dc). The
		// renderer used to require ListenerUntil, so after the window was removed
		// buildOverview said listening and THIS said the opposite — a migration
		// left half-done, on the one command an agent uses to check "am I still
		// waiting, is this normal?" (CRI2 P1-2).
		gen := ""
		if r.ListenerGeneration > 0 {
			gen = fmt.Sprintf(", generation %d", r.ListenerGeneration)
		}
		since := ""
		if r.ListenerSince != nil {
			since = fmt.Sprintf(", waiting since %s", r.ListenerSince.Format("15:04:05"))
		}
		fmt.Fprintf(w, "listener: listening (PID %d%s%s)\n", r.ListenerPid, since, gen)
		// The surgical command, written next to the number it needs. The PID was
		// already here and neither of us thought to use it when it mattered:
		// one `pkill -f "cab-bridge next"` killed four waiters at once, because
		// the hammer is easier to remember than the scalpel. This does not add
		// information — it makes the information unavoidable.
		fmt.Fprintf(w, "          to stop just this one: kill %d\n", r.ListenerPid)
	case r.ListenerReclaimPending:
		fmt.Fprintf(w, "listener: reclaim-pending (generation %d — revoked, no listener has re-claimed yet)\n", r.ListenerGeneration)
	default:
		fmt.Fprintln(w, "listener: not listening")
	}

	if r.Peer == nil {
		fmt.Fprintln(w, "peer:  (none paired in this scope yet)")
	} else {
		fmt.Fprintf(w, "peer:  %s  (%s)  role %s  state %s%s  channel ok\n",
			r.Peer.AgentName, r.Peer.SessionID, r.Peer.Role, overviewState(r.Peer.State), overviewLive(r.Peer.Stale))
	}

	if len(r.Inbox) == 0 {
		// "nothing unread", not "empty": inbox/ may well hold read messages that
		// nobody has archived yet, and this line has no business calling that
		// empty. It answers one question — is there anything for me to read? — and
		// says exactly that.
		fmt.Fprintln(w, "inbox: nothing unread")
		return
	}
	fmt.Fprintf(w, "inbox: %d unread\n", len(r.Inbox))
	for _, m := range r.Inbox {
		from := m.FromAgentName
		if from == "" {
			from = m.From
		}
		fmt.Fprintf(w, "       - %s from %s  type %s\n", m.MsgID, from, m.Type)
	}
}

func overviewDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func overviewState(s string) string {
	if s == "" {
		return "idle"
	}
	return s
}

func overviewLive(stale bool) string {
	if stale {
		return "  [stale]"
	}
	return "  [live]"
}
