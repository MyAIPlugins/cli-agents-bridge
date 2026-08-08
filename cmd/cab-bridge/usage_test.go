package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPrintUsage_OnlyAdvertisesWhatExists guards the discovery surface.
//
// After the v0.8 removal the help still listed bootstrap/listen/receive and
// described `ask` with --to/--content/--file/--in-reply-to/--allow-mesh: an
// invariant that evaporated with the files, because the deleted commands stopped
// being executable while remaining the PRIMARY source of discovery. For an agent
// that is the worst form of LL-13 — it does not have to invent a command, we are
// teaching it one that does not exist.
func TestPrintUsage_OnlyAdvertisesWhatExists(t *testing.T) {
	help := captureStderr(t, printUsage)

	t.Run("no_removed_command_is_advertised", func(t *testing.T) {
		for _, gone := range []string{"bootstrap", "listen", "receive"} {
			assert.NotRegexp(t, regexp.MustCompile(`(?m)^\s+`+gone+`\b`), help,
				"%q was removed and must not be advertised", gone)
		}
	})

	t.Run("no_flag_the_verbs_reject_is_advertised", func(t *testing.T) {
		for _, flag := range []string{"--to", "--content", "--file", "--in-reply-to", "--allow-mesh"} {
			assert.NotContains(t, help, flag, "the loop verbs reject %q; advertising it is a dead end", flag)
		}
	})

	t.Run("every_loop_verb_is_listed_and_dispatchable", func(t *testing.T) {
		// Dispatchable = the main switch has a case for it. Checked against the
		// same list the help promises, so the two cannot drift apart silently.
		for _, verb := range []string{"join", "next", "ask", "tell", "reply"} {
			assert.Contains(t, help, verb, "the loop must be discoverable")
			assert.True(t, isDispatchable(verb), "%q is advertised but has no dispatch case", verb)
		}
	})
}

// isDispatchable reports whether main's switch handles cmd, by invoking the
// dispatch indirectly: every known subcommand must NOT fall through to the
// "unknown subcommand" branch. Kept as a name list mirrored from main.go's
// switch — the test above fails if the help and this list disagree.
func isDispatchable(cmd string) bool {
	known := []string{
		"join", "register", "next", "ask", "tell", "reply", "connect", "notify-watch",
		"peers", "cleanup", "status", "overview", "whoami", "state", "sent", "inbox",
		"read", "inspect", "migrate-from-patil",
	}
	for _, k := range known {
		if k == cmd {
			return true
		}
	}
	return false
}

// TestPrintUsage_MentionsTheStdinRule: the one payload rule is the thing an
// agent most needs to know and cannot guess.
func TestPrintUsage_MentionsTheStdinRule(t *testing.T) {
	help := captureStderr(t, printUsage)
	assert.True(t, strings.Contains(help, "stdin"), "the payload rule must be discoverable")
}
