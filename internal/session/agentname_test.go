package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A TYPED name carrying the separator is refused; a DERIVED one is sanitised.
// The two arrive by different routes and deserve different answers: failing a
// join because a worktree happens to be called `feat@2` would be an error for a
// choice the caller never made.
func TestAgentName_TypedIsRefusedDerivedIsSanitised(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateAgentName(""), "no name is not a bad name")
	require.NoError(t, ValidateAgentName("VAL-bridge"))

	err := ValidateAgentName("VAL@home")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "separates a name from its project",
		"the error has to say WHY, or it reads as an arbitrary rule")

	// F-124: the argument is the absolute PATH, and the repair is an ESCAPE, so
	// the original basename can be recovered from the result.
	got, changed := SanitizeDerivedName("/work/feat@2")
	assert.True(t, changed, "the caller must be able to say it happened")
	assert.Equal(t, "feat_402", got, "`@` is 0x40")
	assert.NoError(t, ValidateAgentName(got), "and what comes out must be addressable")

	got, changed = SanitizeDerivedName("/work/esc-v08")
	assert.False(t, changed)
	assert.Equal(t, "esc-v08", got, "an already-safe basename takes no digest")
}

// The invariant lives at the ONE place every registration passes through, plus
// the one other door that writes AgentName.
//
// Validating in `join` and `register` — the two the author had in hand — would
// have been the very mistake the design gate had just caught in the plan:
// checking the doors you happen to hold and calling the result a property of the
// system. `join` and `register` both come through Register; RenameAgent does not.
func TestRegisterAndRename_HoldTheInvariantAtThePoint(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, time.Second)
	proj := filepath.Join(dir, "proj")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	_, _, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: proj, AgentName: "VAL@home", Role: RoleVal})
	require.Error(t, err, "a typed name with the separator is refused at the point, not at the caller")
	assert.Contains(t, err.Error(), "cannot contain")

	mf, release, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: proj, AgentName: "VAL-ok", Role: RoleVal})
	require.NoError(t, err)
	require.NoError(t, release())

	require.Error(t, mgr.RenameAgent(mf.SessionID, "VAL@elsewhere"),
		"the fourth door: RenameAgent writes AgentName without passing through Register")
	after, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, "VAL-ok", after.AgentName, "and a refused rename leaves the name alone")

	// A DERIVED name is sanitised instead, so a directory nobody chose cannot
	// fail a registration.
	odd := filepath.Join(dir, "feat@2")
	require.NoError(t, os.MkdirAll(odd, 0o700))
	mf2, release2, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: odd, Role: RoleEsc})
	require.NoError(t, err, "nobody typed the directory's name")
	require.NoError(t, release2())
	assert.Equal(t, "feat_402", mf2.AgentName)
	assert.NoError(t, ValidateAgentName(mf2.AgentName))
}

// P1-1 of the diff gate: the writer sanitised the derived name and the READER
// kept the raw basename, so `--resume` in a directory called `feat@2` looked for
// a session named `feat@2`, found none, and registered a SECOND one. If the
// first held mail, the peer came back to an empty inbox with its work in the
// other session.
//
// The canonical shape — a writer's semantics changed without re-examining its
// readers — and the comment on findIdentityMatches said the two used "the SAME
// defaults", which was true until it silently was not.
func TestResume_FindsTheSessionRegisteredWithASanitisedName(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, time.Second)
	proj := filepath.Join(dir, "feat@2")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	first, release, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: proj, Role: RoleEsc, Scope: proj})
	require.NoError(t, err)
	require.NoError(t, release())
	require.Equal(t, "feat_402", first.AgentName)

	again, release2, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: proj, Role: RoleEsc, Scope: proj, Resume: true})
	require.NoError(t, err)
	require.NoError(t, release2())

	assert.Equal(t, first.SessionID, again.SessionID,
		"a resume must find the session the writer actually created, not one named after the raw directory")

	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "and never leave a second session holding the first one's mail")
}

// F-3b: the FOURTH pen. A v1 manifest takes its agent name from ProjectName at
// load time, so a directory carrying the separator produced an unaddressable
// name on every load — from the new binary, today. Three doors had been counted
// and there were four.
func TestApplyV1Defaults_SanitisesTheDerivedName(t *testing.T) {
	t.Parallel()
	mf := &Manifest{ProjectName: "feat@2"}
	mf.ApplyV1Defaults()
	assert.Equal(t, "feat-2", mf.AgentName)
	assert.NoError(t, ValidateAgentName(mf.AgentName), "every pen must produce an addressable name")
}

// --- F-124 lotto 1: a name has to survive the shell that carries it ---------

// TestValidateAgentName_TheGrammar pins the whole rule, refusals included,
// because the property is not "no spaces" — it is that a name must reach the
// binary as ONE argument and be read as a recipient once it does.
//
// Two clauses, and they are not the same clause twice:
//   - the shell must deliver it whole. UNQUOTED, `cab-bridge tell ESC bridge`
//     splits, and with an agent called `ESC` alive it delivered the six bytes of
//     the second half TO THAT AGENT, exit 0. Quoted it arrives intact and is
//     refused by policy instead — a recipient that only works when it is quoted
//     is a recipient that will be pasted wrong. (The first version of this
//     comment said `tell 'ESC bridge'` was the one that split, which is backwards:
//     the quotes are what prevent it. CRI diff-gate P2-3.)
//   - the verbs must read it as a positional. `cab-bridge tell -foo hi` is
//     refused at verbs.go:490 before any lookup, so a name starting with `-` is
//     unaddressable however carefully it is quoted (CRI design-gate #2).
func TestValidateAgentName_TheGrammar(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"", "VAL-bridge", "a", "_x", "x.y-z", "ESC2", "VAL_bridge"} {
		assert.NoError(t, ValidateAgentName(ok), "%q is addressable", ok)
	}

	// The shell clause.
	for _, bad := range []string{"ESC bridge", "a\tb", "a\nb", "it's", "back`tick", "a$b", "a;b", "a*b", `a\b`, "a|b"} {
		err := ValidateAgentName(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "ONE argument",
			"the reason is the shell, and an error that does not say so reads as an arbitrary rule")
	}

	// The positional clause: a leading dash survives any quoting and still loses.
	err := ValidateAgentName("-dash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flag", "the reason here is the CLI grammar, not the shell — a different cause deserves a different sentence")

	// And `@` keeps the reason it already had: the addressing grammar, not the
	// shell. Two causes, two sentences.
	err = ValidateAgentName("VAL@home")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "separates a name from its project")
}

// TestSanitizeDerivedName_EveryRuleIsDecided covers the derived half, where a
// refusal is not available: nobody typed the directory, so the answer is a
// repair. Each row is a rule that cannot be read off the code.
func TestSanitizeDerivedName_EveryRuleIsDecided(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path, want string
		changed    bool
		why        string
	}{
		{"/w/esc-v08", "esc-v08", false, "an already-safe base comes back untouched: the ordinary case pays nothing"},
		{"/w/my repo", "my_20repo", true, "the ordinary repair: a space is 0x20"},
		{"/w/feat@2", "feat_402", true, "and `@` is 0x40 — it used to become `-`, which was not reversible"},
		{"/w/a   b", "a_20_20_20b", true, "no collapsing: a run of three has to come back as three"},
		{"/w/-dash-", "_2Ddash-", true, "only the HEAD is restricted, so the trailing dash stays"},
		{"/w/.hidden", "_2Ehidden", true, "a leading dot escapes for the same reason a dash does"},
		{"/w/caffè", "caff_C3_A8", true, "one escape per BYTE, so the inverse needs no knowledge of encodings"},
		{"/w/---", "_2D--", true, "three dashes are a fine name once the head is escaped"},
		{"/w/日本", "_E6_97_A5_E6_9C_AC", true, "the degenerate case: ugly, and nobody typed it"},
	} {
		got, changed := SanitizeDerivedName(tc.path)
		assert.Equal(t, tc.want, got, "%q: %s", tc.path, tc.why)
		assert.Equal(t, tc.changed, changed, "%q: the caller can only say it happened if we tell it", tc.path)
		assert.NoError(t, ValidateAgentName(got), "%q: whatever comes out must be addressable", tc.path)
	}
}

// P1-2 of the diff gate, and it is the regression this lot introduced: `a+b` and
// `a:b` were DISTINCT before the grammar and both reduce to `a-b` after it —
// one substitution each, so the run-collapse was never the cause. Reproduced
// through the public `register` at the time: two live sessions answering to one
// name, and `tell a-b` exiting 1 with "2 live agents are named a-b".
//
// A first attempt used a 16-bit digest of the path. It was NOT enough, and the
// reason is worth keeping: distinct inputs do not give distinct outputs, because
// what limits a function is its CODOMAIN. The critic built two real paths sharing
// `e051` and registered two live `a-b-e051`. Only an inverse makes a function
// injective, so the derivation escapes instead of hashing.
//
// This asserts the PROPERTY over a generated corpus, not a pair I chose: a pair
// proves that those two differ, which is exactly what the digest also satisfied.
func TestSanitizeDerivedName_IsInjectiveOverACorpus(t *testing.T) {
	t.Parallel()

	alphabet := []string{"a", "_", "+", "-", ".", "0", " ", "é", "@", "Z"}
	var corpus []string
	var gen func(string, int)
	gen = func(prefix string, depth int) {
		if prefix != "" {
			corpus = append(corpus, prefix)
		}
		if depth == 0 {
			return
		}
		for _, c := range alphabet {
			gen(prefix+c, depth-1)
		}
	}
	gen("", 3)
	require.Greater(t, len(corpus), 1000, "the corpus has to be big enough to be worth calling one")

	seen := make(map[string]string, len(corpus))
	for _, base := range corpus {
		got, _ := SanitizeDerivedName("/repo/" + base)
		require.NoError(t, ValidateAgentName(got), "%q derived %q, which is not addressable", base, got)
		if prev, dup := seen[got]; dup {
			t.Fatalf("collision: %q and %q both derive %q", prev, base, got)
		}
		seen[got] = base
	}
	assert.Len(t, seen, len(corpus), "distinct directories, distinct names — no exceptions in the corpus")

	// The case that kills this idea if the marker is not escaped first: a
	// directory named exactly like another one's encoding.
	plus, _ := SanitizeDerivedName("/repo/a+b")
	literal, _ := SanitizeDerivedName("/repo/" + plus)
	assert.Equal(t, "a_2Bb", plus)
	assert.Equal(t, "a_5F2Bb", literal, "the marker escapes FIRST, or these two are one name")

	// Stable: the agent re-reads its name from `join`, so it must not move.
	again, _ := SanitizeDerivedName("/repo/a+b")
	assert.Equal(t, plus, again, "the same directory always derives the same name")

	// WHAT IS NOT FIXED, pinned so the limit is a decision and not a surprise:
	// two different projects whose basenames are ALREADY identical still derive
	// one name. That collided before this change (Manager.Register has never
	// enforced name uniqueness) and closing it is a separate lot.
	w1, _ := SanitizeDerivedName("/repo/alpha/work")
	w2, _ := SanitizeDerivedName("/repo/beta/work")
	assert.Equal(t, w1, w2, "unchanged from before: identical basenames still share a name")
}

// The fallback has to live INSIDE SanitizeDerivedName, not in one caller.
//
// deriveAgentName (naming.go) already guards against an empty base, but it
// guards its own call site only: derivedAgentName here would have taken the
// empty string.
func TestSanitizeDerivedName_FallbackCoversEveryCaller(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "_2D--", derivedAgentName("/tmp/---"),
		"three dashes are a perfectly good name once the HEAD is escaped: the grammar only restricts the front")
	assert.NoError(t, ValidateAgentName(derivedAgentName("/tmp/---")))
}

// P1-1 of the diff gate: the v1 read-default is FROZEN on the @-only algorithm.
//
// LoadManifest applies it on every read and touchHeartbeat is load-modify-save,
// so anything computed here is persisted at the first heartbeat with no
// FormerAgentNames. Widening it would have changed a name that `peers` was
// already showing and the resolver was already matching — once, silently, and
// irreversibly at the next write.
func TestApplyV1Defaults_IsFrozenOnTheHistoricAlgorithm(t *testing.T) {
	t.Parallel()

	mf := &Manifest{ProjectName: "my repo"}
	mf.ApplyV1Defaults()
	assert.Equal(t, "my repo", mf.AgentName,
		"a v1 name stays exactly as addressable, or unaddressable, as it has always been")

	// The one repair it has always done is still done.
	sep := &Manifest{ProjectName: "feat@2"}
	sep.ApplyV1Defaults()
	assert.Equal(t, "feat-2", sep.AgentName, "the @-only rule is the one this pen was given")

	// And no digest: this is not a derivation from a path, it is a read-default.
	assert.NotRegexp(t, `-[0-9a-f]{4}$`, sep.AgentName)
}

// The consumer test the diff gate asked for: v1 load -> heartbeat -> disk.
//
// The unit assertion above says ApplyV1Defaults computes the historic name. This
// one says what happens to it, which is the part that made the widened version a
// P1: LoadManifest applies the default, touchHeartbeat is load-modify-save, so
// the computed value is WRITTEN — and if the algorithm had widened, that write
// would have replaced a name peers were already using, with no FormerAgentNames
// and no way back.
func TestApplyV1Defaults_HeartbeatPersistsTheHistoricName(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, time.Second)

	const sid = "v1sess01"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions", sid), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "sessions", sid, "manifest.json"),
		[]byte(`{"sessionId":"`+sid+`","schemaVersion":1,"projectName":"my repo","projectPath":"/w/my repo"}`),
		0o600))

	loaded, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	require.Equal(t, "my repo", loaded.AgentName, "the read-default is the historic one")

	// The write that made this a P1 rather than a curiosity.
	require.NoError(t, mgr.Touch(sid))

	raw, err := os.ReadFile(filepath.Join(dir, "sessions", sid, "manifest.json"))
	require.NoError(t, err)
	// Parsed, not substring-matched: the encoder indents, so a literal
	// `"agentName":"my repo"` would fail on formatting and say nothing about the
	// value — a red that means the wrong thing is as useless as a green that does.
	var onDisk Manifest
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	assert.Equal(t, "my repo", onDisk.AgentName,
		"the heartbeat materialised the name — it must be the one the old binary showed, not a repaired one")
	assert.Empty(t, onDisk.FormerAgentNames,
		"and nothing was renamed, so there is no history to invent")
}
