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
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// overviewReport is the F-42 at-a-glance view of a session's world in ONE call:
// who I am, my paired peer (the complementary role in my scope), and my pending
// inbox — all resolved from the cwd, no --session-id to type or transcribe.
// Worktree-aware by construction: "me" comes from the cwd lookup on ProjectPath,
// the peer from the shared scope (F-41 makes a VAL at the main repo and an ESC
// in a worktree of the same repo share that scope). It reuses the existing
// building blocks (LongestPrefixLookup, collectPeers/selectPeer, collectInbox)
// rather than duplicating them.
type overviewReport struct {
	Me overviewSelf `json:"me"`

	// F-81: listener observability — whether THIS session is actively in a listen
	// window and when it expires. ListenerActive is IsProcessAlive(PID) AND a
	// future ListenUntil; pid/until are only meaningful (and only emitted) when
	// active. Answers the CRI ask "am I really listening? PID X, expires in Y?"
	// that PID/heartbeat/state alone could not.
	ListenerActive bool       `json:"listenerActive"`
	ListenerPid    int        `json:"listenerPid,omitempty"`
	ListenerUntil  *time.Time `json:"listenerUntil,omitempty"`

	Peer  *overviewPeer `json:"peer"`  // null when no complementary peer is registered yet
	Inbox []overviewMsg `json:"inbox"` // pending messages only (inbox/, not processed/)
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

	// F-81: am I actively listening? A live PID AND a future listen window. AND'd
	// so a dead listen (PID gone after the process exits) or an expired window
	// both read as "not listening" — no false positive from a stale ListenUntil
	// left in the manifest. listen writes ListenUntil at startup (SetListenUntil).
	if session.IsProcessAlive(me.PID) && me.ListenUntil != nil && me.ListenUntil.After(now) {
		report.ListenerActive = true
		report.ListenerPid = me.PID
		report.ListenerUntil = me.ListenUntil
	}

	// Peer: the complementary role within my own scope+team. collectPeers
	// includes me, but selectPeer never picks my own role, so I am not selected.
	// Filtering on MY stored scope (not resolveScope(cwd)) keeps this correct even
	// for an inherited/legacy scope, though F-41 makes them equal for a worktree.
	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, true, me.TeamID, me.Scope)
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

	// Pending inbox: collectInbox is the same pure read `inbox --list` uses; keep
	// only the inbox/ box (processed/ is already handled).
	entries, err := collectInbox(filepath.Join(cfg.DataDir, "sessions", sid), cfg.MaxMessageBytes)
	if err != nil {
		return overviewReport{}, fmt.Errorf("overview: read inbox: %w", err)
	}
	for _, e := range entries {
		if e.Box != "inbox" {
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
	if r.ListenerActive && r.ListenerUntil != nil {
		fmt.Fprintf(w, "listener: listening (PID %d, expires in %s)\n", r.ListenerPid, time.Until(*r.ListenerUntil).Truncate(time.Second))
	} else {
		fmt.Fprintln(w, "listener: not listening")
	}

	if r.Peer == nil {
		fmt.Fprintln(w, "peer:  (none paired in this scope yet)")
	} else {
		fmt.Fprintf(w, "peer:  %s  (%s)  role %s  state %s%s  channel ok\n",
			r.Peer.AgentName, r.Peer.SessionID, r.Peer.Role, overviewState(r.Peer.State), overviewLive(r.Peer.Stale))
	}

	if len(r.Inbox) == 0 {
		fmt.Fprintln(w, "inbox: empty")
		return
	}
	fmt.Fprintf(w, "inbox: %d pending\n", len(r.Inbox))
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
