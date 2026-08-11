package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// WHAT THIS FILE IS FOR, and why the obvious version of it is worthless.
//
// internal/shellarg proves the renderer as a property. That says nothing about
// whether a producer CALLS it. The first attempt at this file did
// `evaluate(shellarg.Quote(hostile))` under the name "producers are wired": it
// verified the renderer a second time and would have stayed green with every
// call site disconnected — "the mechanism exists -> the producer uses it", which
// is precisely the inference it existed to forbid (CRI re-gate, P2).
//
// So nothing here calls shellarg. Each test runs a REAL producer, takes the text
// it emitted, and hands that text to /bin/sh. If a call site drops the renderer,
// the shell splits the token or refuses the line, and the test goes red without
// knowing anything about how the rendering is done.
//
// The preexisting tests do not cover this either, and it is worth saying why:
// they all use shell-SAFE fixtures — t.TempDir() paths, roles like `esc`,
// current-grammar names — so they exercise the branch and never the hazard.
const (
	// A directory named after its owner: one space, one apostrophe. Ordinary
	// enough to appear on a real machine, hostile enough that an unrendered
	// producer cannot pass by accident.
	wiringHostileDir = "Alan's twin"
	// Roles are NOT an enum (internal/routing/role.go: "Validation is structural,
	// not enumerated"), so a two-word role reaches these messages.
	wiringHostileRole = "browser tester"
)

// emittedCommand extracts the command a producer printed, starting at `start`
// and ending at the newline or the backtick that closes it in prose.
func emittedCommand(t *testing.T, text, start string) string {
	t.Helper()
	i := strings.Index(text, start)
	require.GreaterOrEqual(t, i, 0, "no command starting %q was emitted in:\n%s", start, text)
	cmd := text[i:]
	if j := strings.IndexAny(cmd, "\n`"); j >= 0 {
		cmd = cmd[:j]
	}
	// Placeholders are for a human to fill in. The known ones are substituted;
	// an unknown one is refused rather than passed through, because `<foo>` is a
	// REDIRECTION to a shell — letting it reach /bin/sh would turn a new
	// placeholder into a puzzling failure instead of this sentence.
	for _, p := range []struct{ from, to string }{{"<name>", "NEWNAME"}, {"<id>", "abcd1234"}} {
		cmd = strings.ReplaceAll(cmd, p.from, p.to)
	}
	cmd = strings.TrimSpace(cmd)
	require.False(t, strings.ContainsAny(cmd, "<>"),
		"unknown placeholder in %q — add it to the substitutions in emittedCommand", cmd)
	return cmd
}

// runEmitted evaluates that command the way a reader pastes it, against a stand-in
// for its first word that reports the argv it was given.
func runEmitted(t *testing.T, text, start string) []string {
	t.Helper()
	cmd := emittedCommand(t, text, start)

	dir := t.TempDir()
	word0 := strings.Fields(cmd)[0]
	require.NoError(t, os.WriteFile(filepath.Join(dir, word0),
		[]byte("#!/bin/sh\nprintf '%s\\0' \"$@\"\n"), 0o700))

	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = []string{"PATH=" + dir} // nothing from the ambient environment participates
	out, err := c.Output()
	require.NoError(t, err, "the shell refused the emitted command — the producer did not render it: %s", cmd)
	return splitNUL(string(out))
}

// evalWord evaluates a single emitted token: the address lists and the peers
// column offer words to copy, not commands to run.
func evalWord(t *testing.T, word string) []string {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", `printf '%s\0' `+word).Output()
	require.NoError(t, err, "the shell refused the emitted token %q", word)
	return splitNUL(string(out))
}

func splitNUL(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\x00"), "\x00")
}

// emittedToken returns the address offered by a disambiguation line, which has
// the shape "    <address>  (<session id>)".
func emittedToken(t *testing.T, text, containing string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		i := strings.LastIndex(l, "  (")
		if i < 0 || !strings.Contains(l, containing) {
			continue
		}
		return l[:i]
	}
	require.FailNow(t, "no address line", "nothing containing %q in:\n%s", containing, text)
	return ""
}

// canonicalProject returns a fresh project directory under the path `join` will
// actually compute for it.
//
// On macOS t.TempDir() lives under /var, which resolveScope canonicalises to
// /private/var — so a fixture planted with the raw path sits in a DIFFERENT
// scope from the join under test. That is not hypothetical: the live-namesake
// test below was GREEN through the wrong branch (name-taken-in-another-project,
// already covered elsewhere) until this helper existed.
func canonicalProject(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(t.TempDir())
	require.NoError(t, err)
	return resolveScope(abs)
}

func rowFor(t *testing.T, table, id string) string {
	t.Helper()
	for _, line := range strings.Split(table, "\n") {
		if strings.Contains(line, id) {
			return line
		}
	}
	require.FailNow(t, "no row", "no row for %q in:\n%s", id, table)
	return ""
}

// TestWiring_TheFixturesWouldBreakAnUnrenderedProducer is the negative half, and
// without it every assertion in this file is vacuous: if the raw values survived
// a shell intact, the tests below would pass with the renderer disconnected.
func TestWiring_TheFixturesWouldBreakAnUnrenderedProducer(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		wiringHostileDir,
		wiringHostileRole,
		"VAL-x" + session.ScopeSeparator + "/tmp/" + wiringHostileDir,
		"--role=" + wiringHostileRole,
	} {
		out, err := exec.Command("/bin/sh", "-c", `printf '%s\0' `+raw).Output()
		if err != nil {
			continue // the shell refused it outright: hostile enough
		}
		assert.NotEqual(t, []string{raw}, splitNUL(string(out)),
			"%q survives a shell unrendered, so it cannot prove anything about rendering", raw)
	}
}

// peers.go: the SCOPE column is where a cross-project address is copied from,
// and it prints the full path exactly when two projects share a basename — which
// is also the only case where that path can carry a space.
func TestWiring_PeersScopeColumn(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	base := t.TempDir()
	plain := filepath.Join(base, "plain", "repo")
	hostile := filepath.Join(base, wiringHostileDir, "repo")
	plantSessionFull(t, dataDir, "plainaaa", session.RoleVal, "VAL-plain", plain, plain, "")
	plantSessionFull(t, dataDir, "hostilaa", session.RoleVal, "VAL-hostile", hostile, hostile, "")

	table := captureStdout(t, func() { require.NoError(t, runPeers([]string{"--all-scopes"})) })

	row := rowFor(t, table, "hostilaa")
	q := strings.Index(row, "'")
	require.GreaterOrEqual(t, q, 0, "the SCOPE column carries the path unrendered:\n%s", row)
	argv := evalWord(t, row[q:]) // the scope is the last column, so this is the whole token
	require.Len(t, argv, 1, "one argument, separator and all")
	assert.Equal(t, hostile, argv[0])

	assert.NotContains(t, rowFor(t, table, "plainaaa"), "'",
		"an ordinary path must come out untouched — the table reads as it always did")

	// And the machine-readable side must NOT be rendered: quoting is display and
	// remediation, never data.
	jsonOut := captureStdout(t, func() { require.NoError(t, runPeers([]string{"--all-scopes", "--json"})) })
	assert.Contains(t, jsonOut, hostile, "--json carries the scope raw")
	assert.NotContains(t, jsonOut, `'\''`, "--json must never carry the shell rendering")
}

// cleanup.go: the remediation for `--scope=global --force` names the data dir,
// and a data dir comes from $HOME — a place with spaces in it is unremarkable.
func TestWiring_CleanupGlobalRemediation(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), wiringHostileDir)
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)

	err := runCleanup([]string{"--scope=global", "--force"})
	require.Error(t, err)
	argv := runEmitted(t, err.Error(), "cab-bridge cleanup")
	assert.Contains(t, argv, "--data-dir="+dataDir, "the data dir must arrive as ONE argument")
}

// verbs.go: "too many arguments" reprints the recipient, which is the qualified
// address the sender just pasted from somewhere else.
func TestWiring_SendVerbTooManyArguments(t *testing.T) {
	t.Parallel()
	addr := "VAL-x" + session.ScopeSeparator + "/tmp/" + wiringHostileDir
	err := runSendVerb("tell", "query", []string{addr, "one", "two"}, strings.NewReader(""), io.Discard, io.Discard)
	require.Error(t, err)

	argv := runEmitted(t, err.Error(), "cab-bridge tell")
	assert.Contains(t, argv, addr, "the recipient must survive as one argument")
}

// join.go, the legacy-name repair: the role travels into the command that fixes
// the session in place.
func TestWiring_JoinRepairsALegacyName(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	proj := canonicalProject(t)
	plantSessionFull(t, dataDir, "legacyaa", session.RoleEsc, "ESC bridge", proj, proj, "")

	err := runJoin([]string{"--role=" + wiringHostileRole, "--project-path=" + proj})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be addressed as it is", "the legacy-name branch, not another refusal")

	argv := runEmitted(t, err.Error(), "cab-bridge join")
	assert.Contains(t, argv, "--role="+wiringHostileRole)
	assert.Contains(t, argv, "--agent-name=ESC-bridge")
}

// join.go, the name taken in another project.
func TestWiring_JoinNameTakenInAnotherProject(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	theirs, mine := t.TempDir(), t.TempDir()
	plantSessionFull(t, dataDir, "takenaaa", session.RoleEsc, "ESC-taken", theirs, theirs, "")

	err := runJoin([]string{"--role=" + wiringHostileRole, "--agent-name=ESC-taken", "--project-path=" + mine})
	require.Error(t, err)
	require.Contains(t, err.Error(), "in ANOTHER project", "the cross-project branch, not the live-namesake one")

	argv := runEmitted(t, err.Error(), "cab-bridge join")
	assert.Contains(t, argv, "--role="+wiringHostileRole)
}

// join.go, the LIVE namesake in this same project.
func TestWiring_JoinLiveNamesakeHere(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	proj := canonicalProject(t)
	sub := filepath.Join(proj, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	// Same scope, another directory, alive: the branch that refuses rather than
	// take a working agent's name.
	plantSessionFull(t, dataDir, "livenaaa", session.RoleEsc, "ESC-live", proj, sub, "")

	err := runJoin([]string{"--role=" + wiringHostileRole, "--agent-name=ESC-live", "--project-path=" + proj})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already has a LIVE",
		"this must be the same-project branch — the cross-project one is another test, and a green here through THAT branch would prove nothing new")

	argv := runEmitted(t, err.Error(), "cab-bridge join")
	assert.Contains(t, argv, "--role="+wiringHostileRole)
}

// verbs.go: one name, several projects — the way out is the qualified address,
// so the address is the thing that must be pastable.
func TestWiring_RecipientMatchesSeveralProjects(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	base := t.TempDir()
	plain := filepath.Join(base, "plain", "repo")
	hostile := filepath.Join(base, wiringHostileDir, "repo")
	plantSessionFull(t, dataDir, "selfaaaa", session.RoleVal, "VAL-me", plain, plain, "")
	plantSessionFull(t, dataDir, "dup1aaaa", session.RoleEsc, "ESC-dup", plain, plain, "")
	plantSessionFull(t, dataDir, "dup2aaaa", session.RoleEsc, "ESC-dup", hostile, hostile, "")

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	_, err := resolveRecipientByName(cfg, newSessionManager(cfg), "ESC-dup"+session.ScopeSeparator+"repo", "selfaaaa")
	require.Error(t, err)

	argv := evalWord(t, emittedToken(t, err.Error(), "dup2aaaa"))
	require.Len(t, argv, 1)
	assert.Equal(t, "ESC-dup"+session.ScopeSeparator+hostile, argv[0])
}

// verbs.go: two open asks under one name in two projects, same shape one layer
// down — this list is offered as the way to answer one of them.
func TestWiring_OpenAsksUnderOneNameInTwoProjects(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	plain := filepath.Join(base, "plain", "repo")
	hostile := filepath.Join(base, wiringHostileDir, "repo")

	asks := []openAsk{
		{id: "msg-aaaaaaaaaaaa", from: "dup1aaaa", fromName: "VAL-dup", scope: plain},
		{id: "msg-bbbbbbbbbbbb", from: "dup2aaaa", fromName: "VAL-dup", scope: hostile},
	}
	senders := map[string]string{"dup1aaaa": "VAL-dup", "dup2aaaa": "VAL-dup"}

	_, err := soleSessionNamed("VAL-dup", asks, senders)
	require.Error(t, err)

	argv := evalWord(t, emittedToken(t, err.Error(), "dup2aaaa"))
	require.Len(t, argv, 1)
	assert.Equal(t, "VAL-dup"+session.ScopeSeparator+hostile, argv[0])
}

// verbs.go: "say who" names one of the candidates. Names are shell-safe under
// today's grammar, but sessions registered before it are still on disk — that is
// the whole reason lot 1 exists — and this message reprints whatever it finds.
func TestWiring_SoleSenderNamesALegacyCandidate(t *testing.T) {
	t.Parallel()
	const legacy = "A legacy name" // sorts first, so it is the one offered
	_, err := soleSender(map[string]string{"aaaaaaa1": legacy, "bbbbbbb1": "ZZZ-safe"})
	require.Error(t, err)

	argv := runEmitted(t, err.Error(), "cab-bridge reply")
	assert.Equal(t, []string{"reply", legacy, "..."}, argv,
		"the candidate is one argument and the message placeholder another")
}

// verbs.go: the near-miss guard. `reply VAL-brige < report.md` used to send the
// typo as the answer; the guard names the agent it meant, and that name is a
// command the reader runs next.
func TestWiring_ReplyLookalikeOffersALegacyName(t *testing.T) {
	t.Parallel()
	const legacy = "VAL bridge"
	asks := []openAsk{{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: legacy, when: "2026-08-11T10:00:00Z"}}

	_, _, err := resolveReplyTarget([]string{"VAL bridg"}, asks, []string{legacy}, strings.NewReader("the report"))
	require.Error(t, err)

	argv := runEmitted(t, err.Error(), "reply ")
	assert.Equal(t, []string{legacy, "..."}, argv,
		"the suggested name must arrive whole, not as two words")
}
