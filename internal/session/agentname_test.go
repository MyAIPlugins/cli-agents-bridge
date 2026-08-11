package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
// SEPARATED BY CLASS, and that is the finding rather than a tidiness: the first
// version asserted `Contains("ONE argument")` over every refusal at once, so it
// could not tell a true sentence from a false one. Three successive versions of
// the message claimed a MECHANISM — "the shell splits this before the tool runs"
// — that is true for whitespace and false for `+`, `:`, a wildcard or a
// multi-byte rune, all of which arrive intact. A single Contains over the whole
// class is exactly the test that keeps passing while the sentence is wrong.
func TestValidateAgentName_TheGrammar(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"", "VAL-bridge", "a", "_x", "x.y-z", "ESC2", "VAL_bridge"} {
		assert.NoError(t, ValidateAgentName(ok), "%q is addressable", ok)
	}

	// Class 1 — WHITESPACE. The founding repro: unquoted, the shell really does
	// split it, and `tell ESC bridge` delivered six bytes to an agent called
	// `ESC`, exit 0.
	for _, bad := range []string{"ESC bridge", "a\tb", "a\nb"} {
		err := ValidateAgentName(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "supported grammar", "%q: the rule is what the message states", bad)
	}

	// Class 2 — CHARACTERS THE SHELL DOES NOT TOUCH. `a+b` and `a:b` reach the
	// tool whole, quoted or not; they are refused by POLICY, so the message must
	// not tell the reader their input was split. This is the assertion the
	// earlier tests could not make.
	for _, bad := range []string{"a+b", "a:b", "a,b", "a=b"} {
		err := ValidateAgentName(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "supported grammar", "%q", bad)
		assert.NotContains(t, err.Error(), "is split", "%q is NOT split by any shell — do not tell the reader it was", bad)
		assert.NotContains(t, err.Error(), "before the tool", "%q reached the tool: the tool is printing this", bad)
	}

	// Class 3 — SHELL METACHARACTERS. Neither plainly split nor plainly inert:
	// they are rewritten, expanded or eaten depending on the shell, which is
	// precisely why the message describes the RULE and not the mechanism.
	for _, bad := range []string{"it's", "back`tick", "a$b", "a;b", "a*b", `a\b`, "a|b"} {
		err := ValidateAgentName(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "supported grammar", "%q", bad)
	}

	// Class 4 — NON-ASCII. Arrives intact everywhere; refused because the
	// grammar is deliberately narrow, and the message must not invent a cause.
	for _, bad := range []string{"caffè", "日本", "naïve"} {
		err := ValidateAgentName(bad)
		require.Error(t, err, "%q", bad)
		assert.NotContains(t, err.Error(), "is split", "%q is not split anywhere", bad)
	}

	// Class 5 — the POSITIONAL clause, which quoting cannot rescue.
	err := ValidateAgentName("-dash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flag",
		"a leading dash loses at the CLI grammar, not at the shell: a different cause, a different sentence")

	// Class 6 — `@` keeps the reason it always had: the addressing grammar.
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

// --- the inverse, made executable (CRI re-gate P2) --------------------------

// decodeToken is the inverse of escapeToken, and it exists ONLY here.
//
// Production has no need to decode a derived name; what production needs is the
// GUARANTEE that an inverse exists, because that is what makes the encoding
// injective. A comment claiming "the proof is the inverse" is a sentence with no
// way to fail. Writing the inverse in the test turns that sentence into an
// oracle: if escapeToken ever stops being invertible, this stops round-tripping.
func decodeToken(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != markerByte {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		hi, lo := unhex(s[i+1]), unhex(s[i+2])
		if hi < 0 || lo < 0 {
			return "", false
		}
		b.WriteByte(byte(hi<<4 | lo))
		i += 2
	}
	return b.String(), true
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// TestEscapeToken_IsInvertible is EXHAUSTIVE where exhaustive is possible, which
// is the difference between this and the corpus test above.
//
// The corpus falsifies: it looks for a counterexample among strings someone
// chose. This enumerates every single byte and every pair of bytes — 256 + 65536
// inputs, the complete domain up to length two — so for that domain it does not
// sample the property, it decides it. Beyond length two the inverse argument
// carries the rest: escapeToken is a per-byte map, so a decoder that is correct
// on every one-and-two-byte sequence is correct on their concatenations.
func TestEscapeToken_IsInvertible(t *testing.T) {
	t.Parallel()

	check := func(in string) {
		t.Helper()
		out := escapeToken(in)
		back, ok := decodeToken(out)
		require.True(t, ok, "%q -> %q did not decode at all", in, out)
		require.Equal(t, in, back, "%q -> %q -> %q: the inverse is what makes this injective", in, out, back)
	}

	for i := 0; i < 256; i++ {
		check(string([]byte{byte(i)}))
	}
	for i := 0; i < 256; i++ {
		for j := 0; j < 256; j++ {
			check(string([]byte{byte(i), byte(j)}))
		}
	}

	// Injectivity follows from invertibility, but assert it directly on the
	// single-byte domain too — a reader should not have to take the implication
	// on trust either.
	seen := make(map[string]string, 256)
	for i := 0; i < 256; i++ {
		in := string([]byte{byte(i)})
		out := escapeToken(in)
		if prev, dup := seen[out]; dup {
			t.Fatalf("collision on single bytes: %q and %q both -> %q", prev, in, out)
		}
		seen[out] = in
	}
	assert.Len(t, seen, 256, "256 distinct bytes, 256 distinct encodings")
}
