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
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	"os"
	"syscall"
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
	case "join":
		exitFromErr(runJoin(os.Args[2:]))
	case "register":
		exitFromErr(runRegister(os.Args[2:]))
	case "next":
		exitFromErr(runNext(os.Args[2:]))
	case "ask":
		exitFromErr(runAskVerb(os.Args[2:]))
	case "tell":
		exitFromErr(runTell(os.Args[2:]))
	case "reply":
		exitFromErr(runReply(os.Args[2:]))
	case "connect":
		exitFromErr(runConnect(os.Args[2:]))
	case "notify-watch":
		exitFromErr(runNotifyWatch(os.Args[2:]))
	case "peers":
		exitFromErr(runPeers(os.Args[2:]))
	case "cleanup":
		exitFromErr(runCleanup(os.Args[2:]))
	case "status":
		exitFromErr(runStatus(os.Args[2:]))
	case "overview":
		exitFromErr(runOverview(os.Args[2:]))
	case "whoami":
		exitFromErr(runWhoami(os.Args[2:]))
	case "state":
		exitFromErr(runState(os.Args[2:]))
	case "sent":
		exitFromErr(runSent(os.Args[2:]))
	case "inbox":
		exitFromErr(runInbox(os.Args[2:]))
	case "read":
		exitFromErr(runRead(os.Args[2:]))
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
//	(124 is retired: it meant a receive/listen timeout, and both commands are
//	gone. `next` reports an idle window as exit 0 with a timeout payload — an
//	empty wait is not a failure, and a "command failed" every cycle teaches the
//	agent to ignore real ones.)
//
// Errors are always written to stderr (BUG-7 fix carried across all
// subcommands, not just receive).
func exitFromErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err.Error())
	switch {
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
	fmt.Fprintln(os.Stderr, "The working loop — five commands, no flags:")
	fmt.Fprintf(os.Stderr, "  join --role=%s  Once, at the start: register (idempotent) and see who is here\n", session.RoleNamesWithNote())
	fmt.Fprintln(os.Stderr, "  next                 Then forever: deliver whatever arrived, waiting until it does")
	fmt.Fprintln(os.Stderr, "  ask <agent> [\"msg\"]  Ask something — stays open until they reply")
	fmt.Fprintln(os.Stderr, "  tell <agent> [\"msg\"] Inform — no reply expected")
	fmt.Fprintln(os.Stderr, "  reply [\"msg\"]        Answer whoever asked; closes their open asks")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  With no message argument the text is read from stdin: tell VAL-x < report.md")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Service — inspection and maintenance, never in the loop:")
	fmt.Fprintln(os.Stderr, "  read <msg-id>        Re-read a message, including an archived one")
	fmt.Fprintln(os.Stderr, "  sent                 What I sent and what state it is in for the recipient")
	fmt.Fprintln(os.Stderr, "  peers                Who exists (table or --json)")
	fmt.Fprintln(os.Stderr, "  overview             Me, my peers and my inbox at a glance")
	fmt.Fprintln(os.Stderr, "  whoami               This session's identity")
	fmt.Fprintln(os.Stderr, "  state <value>        Declare what I am doing: idle|working|done|orchestrating")
	fmt.Fprintln(os.Stderr, "  status               Own session counters")
	fmt.Fprintln(os.Stderr, "  inbox --list|--tidy  Inspect without consuming, or archive what is handled")
	fmt.Fprintln(os.Stderr, "  register             Lower-level registration (join is the way in)")
	fmt.Fprintln(os.Stderr, "  connect <peer-id>    Refresh own heartbeat + check a peer is reachable")
	fmt.Fprintln(os.Stderr, "  notify-watch -- <hook>  Run <hook> when mail arrives, for peers with no native push (F-66)")
	fmt.Fprintln(os.Stderr, "  cleanup              Remove own session, or --scope=global")
	fmt.Fprintln(os.Stderr, "  inspect <id>         Print a session manifest")
	fmt.Fprintln(os.Stderr, "  migrate-from-patil   Migrate ~/.claude/session-bridge/ sessions to v2 namespace")
	fmt.Fprintln(os.Stderr, "  version              Show version")
	fmt.Fprintln(os.Stderr, "  help                 Show this help")
}
