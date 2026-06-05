package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/myAIPlugins/cli-agents-bridge/internal/routing"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// runConnect implements `cab-bridge connect <target-session-id>` — the
// cmd-level wiring of BUG-9 (session.Manager.Touch).
//
// Pre-handshake actions:
//   - Refresh OWN session lastHeartbeat (Manager.Touch). Without this,
//     Patil-original had the bug where a long-idle sender appeared stale
//     to the remote at the exact moment of connect, breaking the
//     handshake UX.
//   - Validate target manifest exists and is readable.
//   - routing.ValidateSendPair on (sender, target) roles so an esc cannot
//     "connect" to another esc by mistake (consistent with ask path).
//
// Output: JSON with both manifests' summary fields + handshakeAt
// timestamp on stdout. Caller can chain with `ask` / `receive`.
//
// Why a dedicated connect.go file (not peers --connect):
//   - peers reads multiple sessions; connect targets exactly one. Mixing
//     would muddy peers' read-only contract.
//   - connect carries write side-effect (Touch on own manifest) that
//     peers must not have.
//   - Future v0.3+ enhancements (handshake timeout, optional ping
//     round-trip verification) belong here, not on the read-only peers
//     subcommand.
func runConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sessionIDFlag := fs.String("session-id", "", "sender session ID (default: longest-prefix lookup from cwd)")
	allowMesh := fs.Bool("allow-mesh", false, "allow esc→esc connection (BUG-3 override)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("connect: expected exactly one positional <target-session-id> after flags (usage: connect [--session-id=X] [--allow-mesh] <target>); got %d args", len(rest))
	}
	target := rest[0]
	if err := security.ValidateSessionID(target); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	sid, err := resolveCurrentSession(mgr, "connect", *sessionIDFlag)
	if err != nil {
		return err
	}

	// BUG-9 fix at cmd-level: refresh OWN heartbeat before the remote sees
	// us in any peers listing or stale-detection sweep.
	if err := mgr.Touch(sid); err != nil {
		return fmt.Errorf("connect: touch own heartbeat: %w", err)
	}

	senderMf, err := mgr.LoadManifest(sid)
	if err != nil {
		return fmt.Errorf("connect: load sender manifest: %w", err)
	}
	targetMf, err := mgr.LoadManifest(target)
	if err != nil {
		return fmt.Errorf("connect: load target manifest %q: %w", target, err)
	}

	if err := routing.ValidateSendPair(senderMf.Role, targetMf.Role, *allowMesh); err != nil {
		return err
	}

	report := map[string]interface{}{
		"sender": map[string]interface{}{
			"sessionId":     senderMf.SessionID,
			"role":          senderMf.Role,
			"agentName":     senderMf.AgentName,
			"lastHeartbeat": senderMf.LastHeartbeat,
		},
		"target": map[string]interface{}{
			"sessionId":     targetMf.SessionID,
			"role":          targetMf.Role,
			"agentName":     targetMf.AgentName,
			"lastHeartbeat": targetMf.LastHeartbeat,
		},
		"status": "connected",
	}
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("connect: marshal: %w", err)
	}
	fmt.Println(string(out))
	return nil
}
