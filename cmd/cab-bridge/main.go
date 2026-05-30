// Package main is the cab-bridge CLI multiplexer entry-point.
//
// SC-1 (Security Control P0): syscall.Umask(0o077) is set in init() before any
// file or directory creation. This ensures every file created by cab-bridge
// has mode 0o600 and every directory has mode 0o700 by default, protecting
// session manifests, inbox/outbox messages, and lock files from other-UID
// readers (threat model TM-1).
package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// version is injected at build time via -ldflags "-X main.version=<tag>"
// (GoReleaser from the git tag, Makefile from `git describe`). The "dev"
// default applies only to builds without ldflags (e.g. plain `go build`).
var version = "dev"

func init() {
	syscall.Umask(0o077)
}

// TODO Sprint 1: if any code path needs self-detection of the binary install
// location, use os.Executable() — NOT filepath.Abs(os.Args[0]) which resolves
// against the calling session's CWD (verified Day 0 spike Test 2.2,
// docs/spike-fix4-distribution.md §4 caveat). Currently no path needs this:
// state directory is $HOME-derived via config.DefaultConfig().DataDir.

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	switch cmd {
	case "--version", "-v", "version":
		fmt.Println(version)
	case "--help", "-h", "help":
		printUsage()
	case "register":
		exitFromErr(runRegister(os.Args[2:]))
	case "listen":
		exitFromErr(runListen(os.Args[2:]))
	case "ask":
		exitFromErr(runAsk(os.Args[2:]))
	case "connect":
		exitFromErr(runConnect(os.Args[2:]))
	case "receive":
		exitFromErr(runReceive(os.Args[2:]))
	case "peers":
		exitFromErr(runPeers(os.Args[2:]))
	case "cleanup":
		exitFromErr(runCleanup(os.Args[2:]))
	case "status":
		exitFromErr(runStatus(os.Args[2:]))
	case "whoami":
		exitFromErr(runWhoami(os.Args[2:]))
	case "sent":
		exitFromErr(runSent(os.Args[2:]))
	case "inspect":
		exitFromErr(runInspect(os.Args[2:]))
	case "migrate-from-patil":
		exitFromErr(runMigrate(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "cab-bridge: unknown subcommand %q\n", cmd)
		printUsage()
		os.Exit(2)
	}
}

// exitFromErr centralizes subcommand error -> exit code mapping. Exit codes:
//
//	  0  success (err == nil)
//	  1  general failure
//	  2  validation / routing forbidden
//	  3  cleanup global requires confirm (non-tty)
//	124  receive / listen timeout (coreutils timeout(1) convention)
//
// Errors are always written to stderr (BUG-7 fix carried across all
// subcommands, not just receive).
func exitFromErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err.Error())
	switch {
	case errors.Is(err, transportfs.ErrTimeout):
		os.Exit(124)
	case errors.Is(err, ErrConfirmRequired):
		os.Exit(3)
	default:
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "cab-bridge "+version+" — multi-peer IPC bridge for CLI agent sessions")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  cab-bridge <subcommand> [args...]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  register             Register a new session for the current project")
	fmt.Fprintln(os.Stderr, "  listen               Poll inbox emitting messages as JSON until SIGINT or MaxBlocking timeout")
	fmt.Fprintln(os.Stderr, "  ask                  Send a message to a peer (--to, --content, --file, --in-reply-to, --allow-mesh)")
	fmt.Fprintln(os.Stderr, "  connect <peer-id>    Refresh own heartbeat (BUG-9) + validate peer reachable")
	fmt.Fprintln(os.Stderr, "  receive              Long-poll wait for a reply to a specific message ID")
	fmt.Fprintln(os.Stderr, "  peers                List known peers (table or --json) with role/agent/PID/heartbeat age")
	fmt.Fprintln(os.Stderr, "  cleanup              Cleanup own session (default) or --scope=global (BUG-4 scoped)")
	fmt.Fprintln(os.Stderr, "  status               Show own session status (heartbeat age, inbox/outbox/processed counts)")
	fmt.Fprintln(os.Stderr, "  whoami               Show current session identity (session/agent/role/team/projectPath/scope/dataDir)")
	fmt.Fprintln(os.Stderr, "  sent                 List messages this session has sent (its own outbox, F-9)")
	fmt.Fprintln(os.Stderr, "  inspect <id>         Print session manifest JSON (replaces jq dep)")
	fmt.Fprintln(os.Stderr, "  migrate-from-patil   Migrate ~/.claude/session-bridge/ sessions to v2 namespace")
	fmt.Fprintln(os.Stderr, "  version              Show version")
	fmt.Fprintln(os.Stderr, "  help                 Show this help")
}
