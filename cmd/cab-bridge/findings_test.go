package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/findings"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const findingsHeader = "ts\tlotto\tgate\tcritico\tchiave\tfirst_seen\tfinding\tseverita\tevidenza\tdecisione\tazione\tesito\tmodello\ttoken\tcosto\n"

var findingsNow = time.Date(2026, 11, 8, 10, 0, 0, 123_000_000, time.UTC)

// findingsEnv is a repository, a home and a configuration for the command.
type findingsEnv struct {
	repo string // canonical path of a directory that holds a .git
	home string
	cwd  string
	cfg  config.Config
	now  time.Time
}

func newFindingsEnv(t *testing.T) *findingsEnv {
	t.Helper()
	repo := session.CanonicalizePath(t.TempDir())
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0o755))
	cfg := config.DefaultConfig()
	return &findingsEnv{repo: repo, home: session.CanonicalizePath(t.TempDir()), cwd: repo, cfg: cfg, now: findingsNow}
}

func (e *findingsEnv) deps() findingsDeps {
	return findingsDeps{
		Getwd:      func() (string, error) { return e.cwd, nil },
		Home:       func() (string, error) { return e.home, nil },
		LoadConfig: func() (config.Config, error) { return e.cfg, nil },
		Now:        func() time.Time { return e.now },
	}
}

func (e *findingsEnv) run(stdin string, args ...string) (stdout, stderr string, err error) {
	var out, errb bytes.Buffer
	err = findingsRun(args, strings.NewReader(stdin), &out, &errb, e.deps())
	return out.String(), errb.String(), err
}

func (e *findingsEnv) registry() string { return filepath.Join(e.repo, ".handover", "findings.tsv") }

func validFindingJSON(t *testing.T, override map[string]string) string {
	t.Helper()
	m := map[string]string{
		"lotto": "F-156", "gate": "piano", "critico": "CRI", "chiave": "lock-race",
		"finding": "the lock is advisory", "severita": "Major", "evidenza": "lock.go:12",
		"decisione": "PROPOSE-FIX", "esito": "aperto",
	}
	for k, v := range override {
		m[k] = v
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return string(data)
}

const noNamesLine = "findings: note: no person names configured (findings_denied_names in config.json): names are NOT checked\n"

func TestFindingsAdd_PrintsWhatItRecorded(t *testing.T) {
	e := newFindingsEnv(t)
	stdout, stderr, err := e.run(validFindingJSON(t, nil), "add")
	require.NoError(t, err)

	assert.Equal(t, "recorded lock-race first_seen=2026-11-08T10:00:00.123Z -> "+e.registry()+"\n", stdout)
	assert.Equal(t, noNamesLine, stderr)
	data, err := os.ReadFile(e.registry())
	require.NoError(t, err)
	assert.Equal(t, findingsHeader+"2026-11-08T10:00:00.123Z\tF-156\tpiano\tCRI\tlock-race\t2026-11-08T10:00:00.123Z\tthe lock is advisory\tMajor\tlock.go:12\tPROPOSE-FIX\t\taperto\t\t\t\n", string(data))
}

func TestFindingsAdd_RejectionNamesTheColumnAndFails(t *testing.T) {
	e := newFindingsEnv(t)
	stdout, stderr, err := e.run(validFindingJSON(t, map[string]string{"finding": "password=" + "hunter22", "gate": "nope"}), "add")

	require.Error(t, err)
	var rejected *findings.RejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, "findings: column gate: must be one of: piano, diff\n"+
		"findings: column finding: contains something that looks like secret-assignment\n"+
		"findings: nothing was written", err.Error())
	assert.Empty(t, stdout)
	assert.Equal(t, noNamesLine, stderr)
	assert.NoDirExists(t, filepath.Join(e.repo, ".handover"), "a refused row creates nothing, not even the directory")
}

type secretCase struct{ rule, whole, secret string }

// secretCases builds one fake secret per rule from pieces (D15): no whole
// secret-shaped string sits in the source. secret is the part that must never
// come back in an output; the public prefix and the variable name may.
func secretCases() []secretCase {
	rep := strings.Repeat
	body := rep("Zr4Tq9", 8)
	return []secretCase{
		{"aws-access-key", "AK" + "IA" + rep("7K", 8), rep("7K", 8)},
		{"aws-secret-key", "aws_secret_" + "access_key=" + body[:40], body[:40]},
		{"github-fine-grained-token", "github_" + "pat_" + body, body},
		{"github-token", "gh" + "p_" + body[:36], body[:36]},
		{"anthropic-key", "sk-" + "ant-api03-" + body, body},
		{"openai-key", "sk-" + "proj-" + body, body},
		{"slack-token", "xo" + "xb-" + rep("4", 12), rep("4", 12)},
		{"pem-private-key", "-----BEGIN " + "RSA PRIVATE KEY-----", "RSA PRIVATE KEY"},
		jwtSecretCase(),
		{"secret-assignment", "pass" + "word=" + "hunter22", "hunter22"},
		{"url", "https://" + "example.com/x", "example.com/x"},
		{"email", "a.b@" + "example.org", "a.b@example.org"},
	}
}

// jwtSecretCase is a real token built at run time: the JWT rule decides by
// structure, so a string that merely looks like one does not count. The header is
// public (every token starts with the same one); the payload and the signature
// are the part that must never come back.
func jwtSecretCase() secretCase {
	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := enc([]byte(`{"sub":"x1"}`)) + "." + strings.Repeat("Zr4Tq9", 7)
	return secretCase{"jwt", header + "." + payload, payload}
}

func windowsOf(s string, n int) []string {
	var out []string
	for i := 0; i+n <= len(s); i++ {
		out = append(out, s[i:i+n])
	}
	return out
}

// TestFindingsAdd_NeverEchoesTheSecret: for every kind of secret, the refusal
// names the rule and the column but no window of five characters of the secret
// comes back on stdout, stderr or in the error.
func TestFindingsAdd_NeverEchoesTheSecret(t *testing.T) {
	for _, tc := range secretCases() {
		t.Run(tc.rule, func(t *testing.T) {
			e := newFindingsEnv(t)
			stdout, stderr, err := e.run(validFindingJSON(t, map[string]string{"finding": "see " + tc.whole + " for details"}), "add")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "findings: column finding: contains something that looks like "+tc.rule)
			everything := stdout + stderr + err.Error()
			for _, w := range windowsOf(tc.secret, 5) {
				assert.NotContains(t, everything, w, "a window of the secret came back")
			}
			assert.NoDirExists(t, filepath.Join(e.repo, ".handover"))
		})
	}
}

// The BAR's short-assignment case, at the command: a value of 1 to 4 characters
// is refused and does not come back. The marker is a capital Q, which appears in
// none of the fixed texts of the command: asserted, not assumed, by comparing the
// whole stderr with the exact text expected.
func TestFindingsAdd_ShortAssignmentsAreRefusedAndTheValueDoesNotComeBack(t *testing.T) {
	for _, name := range []string{"password", "secret", "token"} {
		for n := 1; n <= 4; n++ {
			e := newFindingsEnv(t)
			value := strings.Repeat("Q", n)
			stdout, stderr, err := e.run(validFindingJSON(t, map[string]string{"azione": name + "=" + value}), "add")

			require.Error(t, err, "%s=%s", name, value)
			assert.Equal(t, "findings: column azione: contains something that looks like secret-assignment\nfindings: nothing was written", err.Error())
			assert.Equal(t, noNamesLine, stderr, "the premise: nothing but fixed text is printed")
			assert.Empty(t, stdout)
			assert.NotContains(t, stdout+stderr+err.Error(), "Q")
			assert.NoFileExists(t, e.registry())
		}
	}
}

func TestFindingsAdd_SaysItEveryTimeWhenNoNamesAreConfigured(t *testing.T) {
	e := newFindingsEnv(t)
	for i := 0; i < 2; i++ {
		_, stderr, err := e.run(validFindingJSON(t, map[string]string{"chiave": "k" + string(rune('a'+i))}), "add")
		require.NoError(t, err)
		assert.Equal(t, noNamesLine, stderr, "add number %d", i+1)
	}

	e.cfg.FindingsDeniedNames = []string{"Marta"}
	_, stderr, err := e.run(validFindingJSON(t, map[string]string{"chiave": "kc"}), "add")
	require.NoError(t, err)
	assert.Empty(t, stderr, "with names configured there is nothing to warn about")
}

func TestFindingsAdd_ConfiguredNamesAreRefused(t *testing.T) {
	e := newFindingsEnv(t)
	e.cfg.FindingsDeniedNames = []string{"Marta"}
	_, _, err := e.run(validFindingJSON(t, map[string]string{"azione": "ask Marta"}), "add")
	require.Error(t, err)
	assert.Equal(t, "findings: column azione: contains something that looks like person-name\nfindings: nothing was written", err.Error())
}

func TestFindingsAdd_InvalidNamesEntryIsAConfigError(t *testing.T) {
	e := newFindingsEnv(t)
	e.cfg.FindingsDeniedNames = []string{"Marta", " "}
	stdout, _, err := e.run(validFindingJSON(t, nil), "add")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "findings_denied_names[1] is invalid")
	assert.NotContains(t, err.Error(), "Marta")
	assert.Empty(t, stdout)
	assert.NoDirExists(t, filepath.Join(e.repo, ".handover"))
}

func TestFindingsAdd_StdinErrorsAreFixedTextsAndCreateNothing(t *testing.T) {
	e := newFindingsEnv(t)
	for input, want := range map[string]string{
		"":                                  "findings: input is empty",
		"not json":                          "findings: stdin is not valid JSON (byte offset 2)",
		`{"lotto":"x","bogus":"y"}`:         "findings: unknown column; the columns are: lotto, gate,",
		`{"ts":"2026-11-08T10:00:00.000Z"}`: "findings: column ts is written by the command",
		`{"ts":null}`:                       "findings: column ts is written by the command",
		`{"first_seen":null}`:               "findings: column first_seen is written by the command",
		`{} {}`:                             "findings: more than one JSON value on stdin",
	} {
		_, _, err := e.run(input, "add")
		require.Error(t, err, input)
		assert.True(t, strings.HasPrefix(err.Error(), want), "input %q: got %q", input, err.Error())
	}
	assert.NoDirExists(t, filepath.Join(e.repo, ".handover"))
}

func TestFindingsRun_UsageAndUnknownActions(t *testing.T) {
	e := newFindingsEnv(t)

	_, _, err := e.run("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected add or stale")

	_, _, err = e.run("", "remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown action "remove"`)

	_, _, err = e.run("{}", "add", "extra")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "takes no arguments")

	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"add", "-h"}, {"stale", "-h"}} {
		stdout, stderr, err := e.run("", args...)
		require.NoError(t, err, args)
		assert.Empty(t, stdout, args)
		assert.Contains(t, stderr, "cab-bridge findings add", args)
	}
	assert.NoDirExists(t, filepath.Join(e.repo, ".handover"))
}

// The help is the only text that travels with the binary, so it has to say what
// the check covers and what it does not, and it must list the values Validate
// accepts. It is built from the package's own lists: this test pins the contract,
// not the wording of each line.
func TestFindingsRun_HelpSaysWhatIsCheckedAndWhatIsNot(t *testing.T) {
	e := newFindingsEnv(t)

	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"add", "-h"}, {"stale", "--help"}} {
		_, stderr, err := e.run("", args...)
		require.NoError(t, err, args)

		for _, col := range findings.InputColumnInfo() {
			assert.Contains(t, stderr, col.Name, "%v: column %s", args, col.Name)
			for _, v := range col.Allowed {
				assert.Contains(t, stderr, v, "%v: allowed value %s of %s", args, v, col.Name)
			}
		}
		for _, rule := range findings.ContentRuleNames() {
			assert.Contains(t, stderr, rule, "%v: rule %s", args, rule)
		}
		assert.Contains(t, stderr, "NOT a general secret detector", args)
		assert.Contains(t, stderr, "split across the", args)
		assert.Contains(t, stderr, "LAST row", args)
		assert.Contains(t, stderr, "esito chiuso (or respinto)", args)
		assert.Contains(t, stderr, "stale writes nothing in the", args)
	}
	assert.NoDirExists(t, filepath.Join(e.repo, ".handover"))
}

// An error prints the short usage only: the guide is for --help.
func TestFindingsRun_AnErrorPrintsTheShortUsageNotTheGuide(t *testing.T) {
	e := newFindingsEnv(t)

	_, _, err := e.run("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cab-bridge findings add")
	assert.NotContains(t, err.Error(), "NOT a general secret detector")
}

// ---- stale ----------------------------------------------------------------

// seed records findings through the command itself, on a clock that moves.
func (e *findingsEnv) seed(t *testing.T, age time.Duration, chiave, esito string) {
	t.Helper()
	e.now = findingsNow.Add(-age)
	_, _, err := e.run(validFindingJSON(t, map[string]string{"chiave": chiave, "esito": esito}), "add")
	require.NoError(t, err)
	e.now = findingsNow
}

func TestFindingsStale_DaysFlagOverridesConfig(t *testing.T) {
	e := newFindingsEnv(t)
	e.seed(t, 10*24*time.Hour, "old-one", "aperto")
	e.seed(t, time.Hour, "young-one", "aperto")
	e.cfg.FindingsStaleDays = 30

	stdout, stderr, err := e.run("", "stale")
	require.NoError(t, err)
	assert.Equal(t, findingsHeader, stdout, "30 days from the configuration: nothing is that old")
	assert.Equal(t, "findings: note: 0 open for more than 30 days\n", stderr)

	for _, args := range [][]string{{"stale", "--days=5"}, {"stale", "--days", "5"}, {"stale", "-days=5"}} {
		stdout, stderr, err = e.run("", args...)
		require.NoError(t, err, args)
		lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		require.Len(t, lines, 2, args)
		assert.Equal(t, findingsHeader, lines[0]+"\n")
		assert.Contains(t, lines[1], "\told-one\t")
		assert.Equal(t, "findings: note: 1 open for more than 5 days\n", stderr)
	}
}

func TestFindingsStale_UsesTheConfiguredDaysByDefault(t *testing.T) {
	e := newFindingsEnv(t)
	e.seed(t, 10*24*time.Hour, "old-one", "aperto")
	stdout, _, err := e.run("", "stale")
	require.NoError(t, err)
	assert.Contains(t, stdout, "\told-one\t", "findings_stale_days defaults to 7")
}

func TestFindingsStale_NoRegistryPrintsHeaderOnly(t *testing.T) {
	e := newFindingsEnv(t)
	stdout, stderr, err := e.run("", "stale")
	require.NoError(t, err)
	assert.Equal(t, findingsHeader, stdout)
	assert.Equal(t, "findings: note: no registry yet at "+e.registry()+"\n", stderr)
	assert.NoDirExists(t, filepath.Join(e.repo, ".handover"), "stale creates nothing")
}

func TestFindingsStale_DaysBelowOneOrTooLargeIsAnErrorThatNamesItsSource(t *testing.T) {
	e := newFindingsEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"stale", "--days=0"}, "findings: --days must be >= 1"},
		{[]string{"stale", "--days=-3"}, "findings: --days must be >= 1"},
		{[]string{"stale", "--days=36501"}, "findings: --days must be at most 36500"},
	} {
		_, _, err := e.run("", tc.args...)
		require.Error(t, err, tc.args)
		assert.Equal(t, tc.want, err.Error())
	}

	for _, tc := range []struct {
		configured int
		want       string
	}{
		{0, "findings: findings_stale_days must be >= 1"},
		{-1, "findings: findings_stale_days must be >= 1"},
		{36501, "findings: findings_stale_days must be at most 36500"},
	} {
		e.cfg.FindingsStaleDays = tc.configured
		_, _, err := e.run("", "stale")
		require.Error(t, err, tc.configured)
		assert.Equal(t, tc.want, err.Error())
	}

	e.cfg.FindingsStaleDays = 0
	_, _, err := e.run("", "stale", "--days=3")
	require.NoError(t, err, "a good flag wins over a bad configuration")
}

func TestFindingsStale_BadFlagsAndArguments(t *testing.T) {
	e := newFindingsEnv(t)
	_, _, err := e.run("", "stale", "--nope")
	require.Error(t, err)
	_, _, err = e.run("", "stale", "--days=abc")
	require.Error(t, err)
	_, _, err = e.run("", "stale", "extra")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected argument")
}

func TestFindingsStale_PrintsRowsAsStoredOldestFirst(t *testing.T) {
	e := newFindingsEnv(t)
	e.seed(t, 20*24*time.Hour, "zeta", "aperto")
	e.seed(t, 30*24*time.Hour, "alpha", "aperto")
	e.seed(t, 30*24*time.Hour, "closed", "chiuso")

	stdout, stderr, err := e.run("", "stale")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[1], "\talpha\t")
	assert.Contains(t, lines[2], "\tzeta\t")
	for _, l := range lines {
		assert.Equal(t, 14, strings.Count(l, "\t"), "15 tab-separated columns")
	}
	assert.Equal(t, "findings: note: 2 open for more than 7 days\n", stderr)
}

// ---- the repository root (D3) ---------------------------------------------

func TestRoot_ASubdirectoryUsesTheRepositoryRoot(t *testing.T) {
	e := newFindingsEnv(t)
	sub := filepath.Join(e.repo, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	e.cwd = sub

	_, _, err := e.run(validFindingJSON(t, nil), "add")
	require.NoError(t, err)
	assert.FileExists(t, e.registry())
	assert.NoDirExists(t, filepath.Join(sub, ".handover"))
}

// A linked worktree shares the main repository's registry: the root is the
// git-COMMON one, so what a critic recorded in one worktree is seen from the other.
func TestRoot_ALinkedWorktreeUsesTheMainRepository(t *testing.T) {
	e := newFindingsEnv(t)
	worktree := session.CanonicalizePath(t.TempDir())
	gitdir := filepath.Join(e.repo, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(gitdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
	e.cwd = worktree

	_, _, err := e.run(validFindingJSON(t, nil), "add")
	require.NoError(t, err)
	assert.FileExists(t, e.registry(), "the registry is in the main repository")
	assert.NoDirExists(t, filepath.Join(worktree, ".handover"))
}

func TestRoot_NoGitMeansAnErrorAndNothingIsCreated(t *testing.T) {
	e := newFindingsEnv(t)
	bare := session.CanonicalizePath(t.TempDir())
	e.cwd = bare

	for _, args := range [][]string{{"add"}, {"stale"}} {
		_, _, err := e.run(validFindingJSON(t, nil), args...)
		require.Error(t, err, args)
		assert.Equal(t, "findings: "+bare+" is not inside a git repository", err.Error())
	}
	entries, err := os.ReadDir(bare)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was created in a folder that is not a repository")
}

// A dotfiles repository in the home directory is not a project: neither the
// home itself nor a folder under it that has no repository of its own.
func TestRoot_TheHomeDirectoryIsNotAProject(t *testing.T) {
	e := newFindingsEnv(t)
	require.NoError(t, os.Mkdir(filepath.Join(e.home, ".git"), 0o755))

	e.cwd = e.home
	_, _, err := e.run(validFindingJSON(t, nil), "add")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is the home directory, not a project")

	under := filepath.Join(e.home, "notes")
	require.NoError(t, os.Mkdir(under, 0o755))
	e.cwd = under
	_, _, err = e.run(validFindingJSON(t, nil), "add")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not inside a git repository")

	assert.NoDirExists(t, filepath.Join(e.home, ".handover"))
	assert.NoDirExists(t, filepath.Join(under, ".handover"))
}

func TestRoot_DependencyErrorsAreReported(t *testing.T) {
	e := newFindingsEnv(t)
	boom := errors.New("boom")

	deps := e.deps()
	deps.Getwd = func() (string, error) { return "", boom }
	err := findingsRun([]string{"stale"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, deps)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "cannot read the current directory")

	deps = e.deps()
	deps.Home = func() (string, error) { return "", boom }
	err = findingsRun([]string{"stale"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, deps)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "cannot find the home directory")

	deps = e.deps()
	deps.LoadConfig = func() (config.Config, error) { return config.Config{}, boom }
	err = findingsRun([]string{"stale"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, deps)
	require.ErrorIs(t, err, boom)
}

// The lock can be busy: the command says so, plainly, and writes nothing.
func TestFindingsAdd_BusyLockIsAnErrorAndWritesNothing(t *testing.T) {
	e := newFindingsEnv(t)
	deps := e.deps()
	tries := 0
	deps.TryLock = func(string) (func() error, error) { tries++; return nil, findings.ErrBusy }

	var out, errb bytes.Buffer
	err := findingsRun([]string{"add"}, strings.NewReader(validFindingJSON(t, nil)), &out, &errb, deps)
	require.ErrorIs(t, err, findings.ErrBusy)
	assert.Equal(t, "findings: the registry is locked by another process, try again", err.Error())
	assert.Equal(t, 100, tries, "the production retry budget")
	assert.Empty(t, out.String())
	assert.NoFileExists(t, e.registry())
}

// Diff gate round 1 (CRI2 M1): a `.handover/` that existed before has no
// .gitignore unless its owner wrote one, and then git shows the registry. Both
// commands say so, in the one format for notes.
func TestFindings_SayWhenTheExistingDirectoryHasNoGitignore(t *testing.T) {
	const note = "findings: note: .handover/ existed already and has no .gitignore: this command did not add one, make sure git ignores it"
	e := newFindingsEnv(t)
	require.NoError(t, os.Mkdir(filepath.Join(e.repo, ".handover"), 0o700))

	_, stderr, err := e.run(validFindingJSON(t, nil), "add")
	require.NoError(t, err)
	assert.Contains(t, stderr, note)

	_, stderr, err = e.run("", "stale")
	require.NoError(t, err)
	assert.Contains(t, stderr, note)

	require.NoError(t, os.WriteFile(filepath.Join(e.repo, ".handover", ".gitignore"), []byte("*\n"), 0o600))
	_, stderr, err = e.run("", "stale")
	require.NoError(t, err)
	assert.NotContains(t, stderr, "has no .gitignore")
}
