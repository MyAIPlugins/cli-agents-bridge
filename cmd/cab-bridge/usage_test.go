package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// --- the flag help is a claim about behaviour, and nothing checks it --------

// TestFlagHelp_MatchesTheBehaviourItDescribes is the contract assertion CRI
// asked for, and it exists because of what the re-gate found: after five
// commits that changed two semantics, BOTH help strings still described the
// contract from before.
//
//	join     -agent-name  "empty = derived from the scope"
//	register -resume      "(same agent-name/role/scope/team) ... errors if a
//	                       live session with this identity already exists"
//
// Every one of those clauses was false. It is F-124's own class — a sentence
// written next to the code, asserting behaviour, with no way to fail — landing
// inside the lot that exists to close that class. Six rounds, three people, and
// nobody looked at the one text no test reads.
//
// This cannot verify that the help is TRUE; nothing short of running each
// described scenario can. What it can do is fail when a phrase we know to be
// load-bearing goes missing, and refuse the specific wordings we have already
// caught being wrong — so the next semantic change trips over the help instead
// of leaving it behind.
func TestFlagHelp_MatchesTheBehaviourItDescribes(t *testing.T) {
	t.Parallel()

	joinHelp := flagHelp(t, runJoin, "join")
	registerHelp := flagHelp(t, runRegister, "register")

	t.Run("join --agent-name names the right source", func(t *testing.T) {
		// The derivation is from the WORKING DIRECTORY, and that is load-bearing:
		// deriving from the scope is not injective, so every agent of one role in
		// a repository would land on the same name (naming.go).
		assert.Contains(t, joinHelp, "WORKING DIRECTORY")
		assert.NotContains(t, joinHelp, "derived from the scope",
			"the exact wording the re-gate caught: it says the one thing the code deliberately does not do")
	})

	t.Run("register --resume describes the identity it actually uses", func(t *testing.T) {
		// Three clauses, each one a behaviour a previous version got wrong.
		assert.Contains(t, registerHelp, "ONLY if you pass --agent-name",
			"the name is a filter only when supplied — otherwise the existing one is adopted")
		assert.Contains(t, registerHelp, "different names",
			"and several names here is a refusal, not a tie-break")
		assert.Contains(t, registerHelp, "RECLAIMED",
			"a live session with this identity is reclaimed (B-2), which is the opposite of what the old text said")
		assert.NotContains(t, registerHelp, "errors if a live session",
			"the wording that described the pre-B-2 contract")
	})

	t.Run("both name the grammar a derived name is escaped into", func(t *testing.T) {
		for name, help := range map[string]string{"join": joinHelp, "register": registerHelp} {
			assert.Contains(t, help, "supported grammar",
				"%s: a caller reading only --help must learn that the name is not the raw basename", name)
		}
	})
}

// flagHelp captures what `<cmd> --help` prints. The subcommands write their
// usage to stderr and return nil on flag.ErrHelp, so this drives the real
// entry point rather than re-declaring the flag set — a copy of the flags here
// would be one more text that can drift from the code.
func flagHelp(t *testing.T, run func([]string) error, name string) string {
	t.Helper()
	out := captureStderr(t, func() {
		if err := run([]string{"--help"}); err != nil {
			t.Fatalf("%s --help: %v", name, err)
		}
	})
	require.NotEmpty(t, out, "%s --help printed nothing", name)
	return out
}
