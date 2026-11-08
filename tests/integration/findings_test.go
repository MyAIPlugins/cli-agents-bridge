package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The F-156 registry through the real binary: the cases that need a process of
// their own (concurrency, a reader beside the writers) or the exit code of the
// command, which a call to findingsRun does not have.

const (
	findingsHeaderLine = "ts\tlotto\tgate\tcritico\tchiave\tfirst_seen\tfinding\tseverita\tevidenza\tdecisione\tazione\tesito\tmodello\ttoken\tcosto\n"
	noNamesStderr      = "findings: note: no person names configured (findings_denied_names in config.json): names are NOT checked\n"
	busyText           = "locked by another process"

	// A legitimate ErrBusy under 8 processes is retried; these bound the retry.
	// They are limits of the TEST: liveness under load is not a promise of the
	// product (the budget is tuned on a Mac and the constant is where to raise it).
	maxBusyRetries = 20
	busyRetryWait  = 50 * time.Millisecond
)

// findingsRepo is a repository (a directory with a .git) plus a home and a data
// dir that belong to the test, so the binary never touches the real ones.
type findingsRepo struct {
	root string
	env  []string
}

func newFindingsRepo(t *testing.T) *findingsRepo {
	t.Helper()
	root := session.CanonicalizePath(t.TempDir())
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	home := session.CanonicalizePath(t.TempDir())
	return &findingsRepo{root: root, env: []string{
		"CAB_DATA_DIR=" + filepath.Join(home, "cab-data"),
		"HOME=" + home,
		"USERPROFILE=" + home,
	}}
}

func (r *findingsRepo) registry() string { return filepath.Join(r.root, ".handover", "findings.tsv") }
func (r *findingsRepo) handover() string { return filepath.Join(r.root, ".handover") }

// runFindingsBinary runs the binary with a working directory and stdin. It does
// not touch *testing.T, so goroutines can call it.
func runFindingsBinary(bin, dir, stdin string, env []string, args ...string) (stdout, stderr string, exit int, err error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return out.String(), errb.String(), 0, nil
	case errors.As(runErr, &exitErr):
		return out.String(), errb.String(), exitErr.ExitCode(), nil
	default:
		return out.String(), errb.String(), -1, runErr
	}
}

func (r *findingsRepo) run(t *testing.T, stdin string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	stdout, stderr, exit, err := runFindingsBinary(buildBinary(t), r.root, stdin, r.env, args...)
	require.NoError(t, err)
	return stdout, stderr, exit
}

func findingJSON(t testing.TB, override map[string]string) string {
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

type registryRow struct{ lotto, chiave, firstSeen, finding string }

// parseWholeRegistry checks that data is a header and k whole 15-column rows,
// each ending in a newline, and returns the rows. A non-empty problem names the
// first thing wrong (so goroutines can report it without calling t).
func parseWholeRegistry(data []byte) (rows []registryRow, problem string) {
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		return nil, "the content does not end with a newline"
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if lines[0]+"\n" != findingsHeaderLine {
		return nil, "the first line is not the header"
	}
	for i, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) != 15 {
			return nil, fmt.Sprintf("row %d has %d columns", i+1, len(f))
		}
		rows = append(rows, registryRow{lotto: f[1], chiave: f[4], firstSeen: f[5], finding: f[6]})
	}
	return rows, ""
}

type addOutcome struct {
	stderr string
	busy   int // how many times the add was refused as busy before it went through
	err    error
}

// addWithRetry runs one `add`; a busy answer (exit 1 and the fixed text of D4)
// is a legitimate outcome and is retried, at most maxBusyRetries times.
func addWithRetry(bin string, repo *findingsRepo, input string) addOutcome {
	var res addOutcome
	for attempt := 0; attempt <= maxBusyRetries; attempt++ {
		_, stderr, exit, err := runFindingsBinary(bin, repo.root, input, repo.env, "findings", "add")
		res.stderr = stderr
		switch {
		case err != nil:
			res.err = err
			return res
		case exit == 0:
			return res
		case exit == 1 && strings.Contains(stderr, busyText):
			res.busy++
			time.Sleep(busyRetryWait)
		default:
			res.err = fmt.Errorf("add exited %d: %s", exit, stderr)
			return res
		}
	}
	res.err = fmt.Errorf("still busy after %d retries", maxBusyRetries)
	return res
}

func TestFindings_AddThenStaleThroughTheBinary(t *testing.T) {
	r := newFindingsRepo(t)

	stdout, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
	require.Equal(t, 0, exit, stderr)
	assert.True(t, strings.HasPrefix(stdout, "recorded lock-race first_seen="), stdout)
	assert.True(t, strings.HasSuffix(strings.TrimSpace(stdout), "-> "+r.registry()), stdout)
	assert.Equal(t, noNamesStderr, stderr)

	stdout, stderr, exit = r.run(t, "", "findings", "stale", "--days=1")
	require.Equal(t, 0, exit, stderr)
	assert.Equal(t, findingsHeaderLine, stdout, "a row recorded a moment ago is not stale")
	assert.Equal(t, "findings: note: 0 open for more than 1 days\n", stderr)
}

// A key that has been open for a month is listed; closing it takes it off the
// list and keeps the month it has been open since.
func TestFindings_AnOldKeyIsStaleUntilItIsClosed(t *testing.T) {
	r := newFindingsRepo(t)
	require.NoError(t, os.Mkdir(r.handover(), 0o700))
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	seed := findingsHeaderLine + strings.Join([]string{
		old, "F-100", "piano", "CRI", "old-key", old, "an old finding", "Major", "x.go:1", "PROPOSE-FIX", "", "aperto", "", "", "",
	}, "\t") + "\n"
	require.NoError(t, os.WriteFile(r.registry(), []byte(seed), 0o600))

	stdout, _, exit := r.run(t, "", "findings", "stale")
	require.Equal(t, 0, exit)
	assert.Contains(t, stdout, "\told-key\t")

	_, stderr, exit := r.run(t, findingJSON(t, map[string]string{"chiave": "old-key", "esito": "chiuso"}), "findings", "add")
	require.Equal(t, 0, exit, stderr)

	stdout, _, exit = r.run(t, "", "findings", "stale")
	require.Equal(t, 0, exit)
	assert.Equal(t, findingsHeaderLine, stdout, "the current row of the key is closed")

	data, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	rows, problem := parseWholeRegistry(data)
	require.Empty(t, problem)
	require.Len(t, rows, 2)
	assert.Equal(t, old, rows[1].firstSeen, "the closing row keeps the first_seen of the key")
}

// The BAR's short-assignment case on the real binary: exit 1, the registry
// untouched (absent, or the same bytes), and the value does not come back.
func TestFindings_RealBinaryRefusesShortAssignments(t *testing.T) {
	const expectedStderr = noNamesStderr +
		"findings: column azione: contains something that looks like secret-assignment\nfindings: nothing was written\n"

	for _, existing := range []bool{false, true} {
		r := newFindingsRepo(t)
		var before []byte
		if existing {
			_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
			require.Equal(t, 0, exit, stderr)
			var err error
			before, err = os.ReadFile(r.registry())
			require.NoError(t, err)
		}
		for _, name := range []string{"password", "secret", "token"} {
			for n := 1; n <= 4; n++ {
				value := strings.Repeat("Q", n)
				stdout, stderr, exit := r.run(t, findingJSON(t, map[string]string{"azione": name + "=" + value}), "findings", "add")

				require.Equal(t, 1, exit, "%s=%s existing=%v", name, value, existing)
				assert.Equal(t, expectedStderr, stderr, "the premise: only fixed text is printed")
				assert.Empty(t, stdout)
				assert.NotContains(t, stdout+stderr, "Q")
			}
		}
		if existing {
			after, err := os.ReadFile(r.registry())
			require.NoError(t, err)
			assert.Equal(t, before, after, "the registry is byte-identical")
		} else {
			assert.NoDirExists(t, r.handover(), "and nothing was created")
		}
	}
}

func TestFindings_ExitCodesAndHelp(t *testing.T) {
	r := newFindingsRepo(t)

	_, stderr, exit := r.run(t, "", "findings", "--help")
	assert.Equal(t, 0, exit)
	assert.Contains(t, stderr, "cab-bridge findings add")

	_, stderr, exit = r.run(t, "", "findings")
	assert.Equal(t, 1, exit)
	assert.Contains(t, stderr, "expected add or stale")

	_, stderr, exit = r.run(t, "not json", "findings", "add")
	assert.Equal(t, 1, exit)
	assert.Contains(t, stderr, "stdin is not valid JSON")

	_, stderr, exit = r.run(t, "", "findings", "stale", "--days=0")
	assert.Equal(t, 1, exit)
	assert.Contains(t, stderr, "--days must be >= 1")

	assert.NoDirExists(t, r.handover())
}

func TestFindings_NoRepositoryMeansExitOneAndNothingCreated(t *testing.T) {
	r := newFindingsRepo(t)
	bare := session.CanonicalizePath(t.TempDir())

	for _, args := range [][]string{{"findings", "add"}, {"findings", "stale"}} {
		_, stderr, exit, err := runFindingsBinary(buildBinary(t), bare, findingJSON(t, nil), r.env, args...)
		require.NoError(t, err)
		assert.Equal(t, 1, exit, args)
		assert.Contains(t, stderr, "is not inside a git repository", args)
	}
	entries, err := os.ReadDir(bare)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestFindings_ConcurrentProcessesWriteWholeRows: 8 real processes, 10 adds each,
// started together; the first add of each uses the SAME new key. At the end the
// registry is a header and 80 whole rows, each expected row present once, and the
// 8 rows of the shared key carry the same first_seen. An add that is refused as
// busy is retried (a legitimate outcome); how many were seen is reported.
func TestFindings_ConcurrentProcessesWriteWholeRows(t *testing.T) {
	const processes, perProcess = 8, 10
	r := newFindingsRepo(t)
	bin := buildBinary(t)

	expected := map[string]string{} // lotto/chiave -> finding
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	outcomes := make([]addOutcome, 0, processes*perProcess)

	for p := 0; p < processes; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			<-start
			for j := 0; j < perProcess; j++ {
				lotto := fmt.Sprintf("w%d", p)
				chiave := fmt.Sprintf("w%d-k%d", p, j)
				if j == 0 {
					chiave = "shared-key"
				}
				finding := fmt.Sprintf("finding %d-%d", p, j)
				input := `{"lotto":"` + lotto + `","gate":"piano","critico":"CRI","chiave":"` + chiave +
					`","finding":"` + finding + `","severita":"Nit","evidenza":"x.go:1","decisione":"CONTEST","esito":"aperto"}`
				out := addWithRetry(bin, r, input)
				mu.Lock()
				expected[lotto+"/"+chiave] = finding
				outcomes = append(outcomes, out)
				mu.Unlock()
			}
		}(p)
	}
	close(start)
	wg.Wait()

	busy := 0
	for _, o := range outcomes {
		require.NoError(t, o.err)
		busy += o.busy
	}
	t.Logf("%d adds, %d of them were refused as busy at least once (%d busy answers in total)", len(outcomes), countBusy(outcomes), busy)

	data, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	rows, problem := parseWholeRegistry(data)
	require.Empty(t, problem)
	require.Len(t, rows, processes*perProcess)
	assert.Equal(t, 1, strings.Count(string(data), findingsHeaderLine), "one header")

	seen := map[string]int{}
	var shared []registryRow
	for _, row := range rows {
		key := row.lotto + "/" + row.chiave
		seen[key]++
		assert.Equal(t, expected[key], row.finding, "the row %s is whole and is the one that was added", key)
		if row.chiave == "shared-key" {
			shared = append(shared, row)
		}
	}
	require.Len(t, seen, processes*perProcess, "every expected row is present")
	for key, n := range seen {
		assert.Equal(t, 1, n, "row %s appears once", key)
	}
	require.Len(t, shared, processes)
	for _, row := range shared {
		assert.Equal(t, shared[0].firstSeen, row.firstSeen, "the 8 rows of the shared key have ONE first_seen")
	}
	assert.Equal(t, []string{".gitignore", "findings.lock", "findings.tsv"}, dirListing(t, r.handover()), "no temporary file is left")
}

func countBusy(outcomes []addOutcome) int {
	n := 0
	for _, o := range outcomes {
		if o.busy > 0 {
			n++
		}
	}
	return n
}

func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// 8 processes find no .handover at all and create it together: one header, one
// .gitignore holding exactly "*\n", 8 rows.
func TestFindings_ConcurrentFirstCreation(t *testing.T) {
	const processes = 8
	r := newFindingsRepo(t)
	bin := buildBinary(t)
	require.NoDirExists(t, r.handover())

	var wg sync.WaitGroup
	start := make(chan struct{})
	outcomes := make([]addOutcome, processes)
	for p := 0; p < processes; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			<-start
			input := `{"lotto":"w` + fmt.Sprint(p) + `","gate":"diff","critico":"CRI2","chiave":"first-` + fmt.Sprint(p) +
				`","finding":"created together","severita":"Nit","evidenza":"x.go:1","decisione":"DECLINE","esito":"respinto"}`
			outcomes[p] = addWithRetry(bin, r, input)
		}(p)
	}
	close(start)
	wg.Wait()
	for _, o := range outcomes {
		require.NoError(t, o.err)
	}
	t.Logf("%d of %d first-creation adds were refused as busy at least once", countBusy(outcomes), processes)

	data, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	rows, problem := parseWholeRegistry(data)
	require.Empty(t, problem)
	assert.Len(t, rows, processes)
	assert.Equal(t, 1, strings.Count(string(data), findingsHeaderLine))
	ignore, err := os.ReadFile(filepath.Join(r.handover(), ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(ignore))
	assert.Equal(t, []string{".gitignore", "findings.lock", "findings.tsv"}, dirListing(t, r.handover()))
}

// isTransientRead says a read failed for a reason that is part of the registry
// being replaced under the reader, and not a defect. Before the reader has ever
// seen the file, "not there yet" is simply the state of the world. After it has,
// the file must never disappear (the rename replaces it atomically): on Unix a
// missing file is a defect, and on Windows it and a sharing violation are
// tolerated while the rename is in flight, a few times in a row at most.
func isTransientRead(err error, seen bool) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return !seen || runtime.GOOS == "windows"
	}
	const errorSharingViolation = syscall.Errno(32)
	return runtime.GOOS == "windows" && errors.Is(err, errorSharingViolation)
}

// TestFindings_ReaderNeverSeesAPartialRegistry is STATISTICAL: a reader that
// reads the file in a loop, and one that runs `stale`, beside 4 writers. Every
// content the reader observes is a header and k whole rows, with k never going
// down. It supports the snapshot-and-rename design; the proof that the registry
// changes only through Publish is TestAdd_ChangesTheRegistryOnlyThroughPublish.
func TestFindings_ReaderNeverSeesAPartialRegistry(t *testing.T) {
	const writers, perWriter = 4, 10
	const maxTransientInARow = 50
	r := newFindingsRepo(t)
	bin := buildBinary(t)

	done := make(chan struct{})
	var readerProblem, staleProblem string
	var reads, nonEmptyReads, staleRuns int
	var readerWG, writerWG sync.WaitGroup

	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		lastRows, inARow, seen := 0, 0, false
		for {
			select {
			case <-done:
				return
			default:
			}
			data, err := security.ReadOwnedFile(r.registry())
			reads++
			if err != nil {
				if seen {
					inARow++
				}
				if !isTransientRead(err, seen) || inARow > maxTransientInARow {
					readerProblem = fmt.Sprintf("read failed (%d in a row after the file was seen): %v", inARow, err)
					return
				}
				continue
			}
			seen, inARow = true, 0
			if len(data) == 0 {
				continue
			}
			rows, problem := parseWholeRegistry(data)
			if problem != "" {
				readerProblem = problem
				return
			}
			if len(rows) < lastRows {
				readerProblem = fmt.Sprintf("the number of rows went down: %d after %d", len(rows), lastRows)
				return
			}
			lastRows = len(rows)
			nonEmptyReads++
		}
	}()

	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(15 * time.Millisecond):
			}
			stdout, stderr, exit, err := runFindingsBinary(bin, r.root, "", r.env, "findings", "stale", "--days=1")
			staleRuns++
			if err != nil || exit != 0 {
				staleProblem = fmt.Sprintf("stale exited %d (%v): %s", exit, err, stderr)
				return
			}
			if !strings.HasPrefix(stdout, findingsHeaderLine) {
				staleProblem = "stale did not print the header first"
				return
			}
		}
	}()

	outcomes := make([]addOutcome, writers*perWriter)
	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(w int) {
			defer writerWG.Done()
			for j := 0; j < perWriter; j++ {
				input := fmt.Sprintf(`{"lotto":"w%d","gate":"piano","critico":"CRI","chiave":"w%d-k%d","finding":"row %d-%d","severita":"Nit","evidenza":"x.go:1","decisione":"CONTEST","esito":"aperto"}`, w, w, j, w, j)
				outcomes[w*perWriter+j] = addWithRetry(bin, r, input)
			}
		}(w)
	}
	writerWG.Wait()
	close(done)
	readerWG.Wait()

	for _, o := range outcomes {
		require.NoError(t, o.err)
	}
	require.Empty(t, readerProblem)
	require.Empty(t, staleProblem)
	t.Logf("the reader read the file %d times (%d with content) and `stale` ran %d times beside %d adds", reads, nonEmptyReads, staleRuns, len(outcomes))

	data, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	rows, problem := parseWholeRegistry(data)
	require.Empty(t, problem)
	assert.Len(t, rows, writers*perWriter)
}
