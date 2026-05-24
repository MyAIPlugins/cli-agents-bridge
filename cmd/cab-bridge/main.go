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

const version = "0.2.0-dev"

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
	case "receive":
		if err := runReceive(os.Args[2:]); err != nil {
			// BUG-7 fix: errors go to stderr, never stdout. Timeout maps
			// to exit code 124 (coreutils timeout(1) convention), other
			// failures exit 1.
			fmt.Fprintln(os.Stderr, err.Error())
			if errors.Is(err, transportfs.ErrTimeout) {
				os.Exit(124)
			}
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "cab-bridge: unknown subcommand %q\n", cmd)
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "cab-bridge "+version+" — multi-peer IPC bridge for CLI agent sessions")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  cab-bridge <subcommand> [args...]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  receive              Wait long-poll for reply to a message (Sprint 2, IMPLEMENTED)")
	fmt.Fprintln(os.Stderr, "  register             Register current session (Sprint 3 planned)")
	fmt.Fprintln(os.Stderr, "  listen               Listen for incoming messages (Sprint 3 planned)")
	fmt.Fprintln(os.Stderr, "  ask <id> <msg>       Send query to peer (Sprint 3 planned)")
	fmt.Fprintln(os.Stderr, "  peers                List known peers (Sprint 3 planned)")
	fmt.Fprintln(os.Stderr, "  cleanup              Cleanup own session (Sprint 3)")
	fmt.Fprintln(os.Stderr, "  status               Show session status (Sprint 3)")
	fmt.Fprintln(os.Stderr, "  inspect <id> --json  Inspect session manifest (Sprint 3)")
	fmt.Fprintln(os.Stderr, "  migrate-from-patil   Migrate sessions from Patil upstream (Sprint 3)")
	fmt.Fprintln(os.Stderr, "  version              Show version")
	fmt.Fprintln(os.Stderr, "  help                 Show this help")
}
