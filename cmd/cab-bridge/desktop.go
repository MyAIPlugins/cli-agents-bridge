package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// desktop is how a val opens the bridge to Claude Desktop, and closes it (F-72,
// D3). The consent is the val's, given at runtime and bounded in time: it is not
// a setting that lives in Desktop's configuration, so installing the connector
// once is enough and a repository is exposed only while its own val says so.
//
// The grant is a file in THIS val's session directory (session.DesktopGrant) and
// nothing else writes it: a re-join of the val neither closes it nor renews it.
// Only another `open` extends it. A val has no heartbeat to prove it is alive,
// so the expiry is what bounds a grant its owner forgot to close.
const desktopUsage = "usage: cab-bridge desktop open|close|status   (run by a val; no flags)"

func runDesktop(args []string) error { return desktopRun(args, os.Stdout, os.Stderr) }

func desktopRun(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		_, err := fmt.Fprintln(stderr, desktopUsage)
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("desktop: expected exactly one of open|close|status, got %d argument(s)\n%s", len(args), desktopUsage)
	}
	verb := args[0]
	if verb != "open" && verb != "close" && verb != "status" {
		return fmt.Errorf("desktop: unknown action %q\n%s", verb, desktopUsage)
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)
	sid, err := resolveCurrentSession(cfg, mgr, "desktop", "")
	if err != nil {
		return err
	}
	mf, err := mgr.LoadManifest(sid)
	if err != nil {
		return fmt.Errorf("desktop: load this session: %w", err)
	}
	// A val and nobody else. Checked on the manifest, the same source the reader
	// side uses: a role someone typed in a message is not evidence of anything.
	if mf.Role != session.RoleVal {
		return fmt.Errorf("desktop: only a val opens the bridge to Claude Desktop — this session (%s) has role %q", mf.AgentName, mf.Role)
	}

	switch verb {
	case "open":
		return desktopOpen(mgr, cfg.MCPGrantTTLMinutes, sid, mf, stdout)
	case "close":
		return desktopClose(mgr, sid, mf, stdout)
	default:
		return desktopStatus(mgr, cfg.StaleSeconds, cfg.MaxMessageBytes, cfg.DataDir, sid, mf, stdout)
	}
}

func desktopOpen(mgr *session.Manager, ttlMinutes int, sid string, mf *session.Manifest, stdout io.Writer) error {
	if ttlMinutes <= 0 {
		return fmt.Errorf("desktop: mcp_grant_ttl_minutes is %d — it must be positive, an open that never expires is not an option here", ttlMinutes)
	}
	ttl := time.Duration(ttlMinutes) * time.Minute
	g, err := mgr.OpenDesktopGrant(sid, ttl)
	if err != nil {
		return fmt.Errorf("desktop: %w", err)
	}
	fmt.Fprintf(stdout, "desktop: the bridge is OPEN to Claude Desktop for %s as %s until %s (%s)\n",
		session.EffectiveScope(mf), mf.AgentName, g.ExpiresAt.Local().Format(time.RFC3339), ttl)
	fmt.Fprintln(stdout, "  Desktop joins with its `join` tool and shows up in `peers` as role architect; it can talk only to a val.")
	fmt.Fprintln(stdout, "  A re-join of this val neither closes this nor renews it. `desktop open` again renews, `desktop close` closes.")
	return nil
}

func desktopClose(mgr *session.Manager, sid string, mf *session.Manifest, stdout io.Writer) error {
	_, wasOpen, rerr := mgr.ReadDesktopGrant(sid)
	if err := mgr.CloseDesktopGrant(sid); err != nil {
		return fmt.Errorf("desktop: %w", err)
	}
	switch {
	case rerr != nil:
		fmt.Fprintf(stdout, "desktop: closed for %s (the grant on disk was unreadable and counted as closed anyway)\n", session.EffectiveScope(mf))
	case wasOpen:
		fmt.Fprintf(stdout, "desktop: the bridge is CLOSED for %s\n", session.EffectiveScope(mf))
	default:
		fmt.Fprintf(stdout, "desktop: the bridge was not open for %s; nothing to close\n", session.EffectiveScope(mf))
	}
	fmt.Fprintln(stdout, "  Messages already in Desktop's inbox stay where they are; they are not shown again unless this val reopens the bridge.")
	return nil
}

func desktopStatus(mgr *session.Manager, staleSeconds, maxContent int, dataDir, sid string, mf *session.Manifest, stdout io.Writer) error {
	scope := session.EffectiveScope(mf)
	now := mgr.NowUTC() // the clock the reader side judges the grant by, so status and MCP agree
	g, found, rerr := mgr.ReadDesktopGrant(sid)
	switch {
	case rerr != nil:
		fmt.Fprintf(stdout, "desktop bridge for %s: UNREADABLE, counted as CLOSED — %v\n", scope, rerr)
	case !found:
		fmt.Fprintf(stdout, "desktop bridge for %s: CLOSED\n", scope)
	case g.ValidAt(now):
		fmt.Fprintf(stdout, "desktop bridge for %s: OPEN until %s (in %s)\n", scope, g.ExpiresAt.Local().Format(time.RFC3339), g.ExpiresAt.Sub(now).Round(time.Second))
	default:
		fmt.Fprintf(stdout, "desktop bridge for %s: EXPIRED at %s — `desktop open` to reopen\n", scope, g.ExpiresAt.Local().Format(time.RFC3339))
	}

	peers, _, perr := collectPeers(mgr, dataDir, staleSeconds, maxContent, true, "", scope)
	if perr != nil {
		return errors.Join(errors.New("desktop: list the architect sessions"), perr)
	}
	var arcs []peerSummary
	for _, p := range peers {
		if p.Role == session.RoleArchitect {
			arcs = append(arcs, p)
		}
	}
	if len(arcs) == 0 {
		fmt.Fprintln(stdout, "  no Desktop session has joined this scope yet")
		return nil
	}
	fmt.Fprintf(stdout, "  Desktop sessions in this scope (%d):\n", len(arcs))
	for _, p := range arcs {
		mark := ""
		if p.Stale {
			mark = "  (stale)"
		}
		fmt.Fprintf(stdout, "    %-24s %s  state=%s  in inbox=%d%s\n", p.AgentName, p.SessionID, orDash(p.State), p.InboxCount, mark)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
