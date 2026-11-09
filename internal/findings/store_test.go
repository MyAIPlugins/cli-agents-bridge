package findings

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 123_000_000, time.UTC)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// newTestStore returns a Store on a fresh .handover path that does NOT exist yet,
// with a clock that stands still and a Sleep that does not sleep.
func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: t0}
	return &Store{
		Dir:   filepath.Join(t.TempDir(), ".handover"),
		Now:   clock.Now,
		Sleep: func(time.Duration) {},
	}, clock
}

func inputFor(chiave string) Input {
	in := validValues()
	in.Chiave = chiave
	return in
}

func mustAdd(t *testing.T, s *Store, in Input) AddResult {
	t.Helper()
	res, err := s.Add(in)
	require.NoError(t, err)
	return res
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func dirNames(t *testing.T, dir string) []string {
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

func TestAdd_WritesAllFifteenColumnsInOrder(t *testing.T) {
	s, _ := newTestStore(t)
	in := inputFor("lock-race")
	in.Azione = "rename the lock file"
	in.Modello = "gpt-5.5"
	in.Token = "43085"
	in.Costo = "0.125"

	res := mustAdd(t, s, in)

	want := header +
		"2026-11-08T10:00:00.123Z\tF-156\tpiano\tCRI\tlock-race\t2026-11-08T10:00:00.123Z\tthe lock is advisory\tMajor\tlock.go:12\tPROPOSE-FIX\trename the lock file\taperto\tgpt-5.5\t43085\t0.125\n"
	assert.Equal(t, want, string(readFile(t, s.RegistryPath())))
	assert.Equal(t, "2026-11-08T10:00:00.123Z", res.Row.FirstSeen)
	assert.Empty(t, res.Notes)
}

func TestAdd_AppendsAndKeepsTheOldBytesVerbatim(t *testing.T) {
	s, clock := newTestStore(t)
	mustAdd(t, s, inputFor("one"))
	before := readFile(t, s.RegistryPath())

	clock.advance(time.Second)
	mustAdd(t, s, inputFor("two"))
	after := readFile(t, s.RegistryPath())

	require.True(t, strings.HasPrefix(string(after), string(before)), "the old registry is a byte-for-byte prefix of the new one")
	assert.Equal(t, 1, strings.Count(string(after), header), "one header, however many rows")
	assert.Equal(t, 3, strings.Count(string(after), "\n"), "header + 2 rows")
}

func TestAdd_ReusesFirstSeenOfTheFirstRowDaysApart(t *testing.T) {
	s, clock := newTestStore(t)
	day0 := formatTimestamp(clock.now)

	mustAdd(t, s, inputFor("shared"))
	clock.advance(3 * 24 * time.Hour)
	second := mustAdd(t, s, inputFor("shared"))
	clock.advance(7 * 24 * time.Hour)
	third := mustAdd(t, s, inputFor("shared"))
	other := mustAdd(t, s, inputFor("another"))

	assert.Equal(t, day0, second.Row.FirstSeen)
	assert.Equal(t, day0, third.Row.FirstSeen)
	assert.NotEqual(t, day0, second.Row.Ts, "ts is always now")
	assert.Equal(t, formatTimestamp(clock.now), third.Row.Ts)
	assert.Equal(t, other.Row.Ts, other.Row.FirstSeen, "a new key starts its own history")
	assert.Equal(t, formatTimestamp(clock.now), other.Row.FirstSeen)
}

func TestAdd_FirstSeenComesFromTheFirstRowEvenIfALaterRowDiffers(t *testing.T) {
	s, clock := newTestStore(t)
	row := func(first string) Row {
		return newRow(inputFor("shared"), "2026-10-01T00:00:00.000Z", first)
	}
	registry := header + row("2026-09-01T00:00:00.000Z").line() + row("2026-09-20T00:00:00.000Z").line()
	require.NoError(t, os.MkdirAll(s.Dir, 0o700))
	require.NoError(t, os.WriteFile(s.RegistryPath(), []byte(registry), 0o600))

	res := mustAdd(t, s, inputFor("shared"))
	assert.Equal(t, "2026-09-01T00:00:00.000Z", res.Row.FirstSeen, "the first row wins, textually")
	assert.Equal(t, formatTimestamp(clock.now), res.Row.Ts)
}

func TestAdd_RefusedRowLeavesTheFileByteIdenticalAndCreatesNothing(t *testing.T) {
	bad := inputFor("lock-race")
	bad.Finding = "password=" + "hunter22"

	// Absent registry, absent directory: a rejection at the pure step creates neither.
	s, _ := newTestStore(t)
	_, err := s.Add(bad)
	var rejected *RejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, []Violation{{Column: "finding", Reason: "contains something that looks like secret-assignment"}}, rejected.Violations)
	assert.NoDirExists(t, s.Dir, "the directory is not created for a row that is refused")

	// Existing registry: same bytes afterwards.
	mustAdd(t, s, inputFor("good"))
	before := readFile(t, s.RegistryPath())
	_, err = s.Add(bad)
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, before, readFile(t, s.RegistryPath()))
	assert.Equal(t, []string{".gitignore", "findings.lock", "findings.tsv"}, dirNames(t, s.Dir), "and no temporary file is left")
}

func TestAdd_NamesConfigurationErrorBlocksAdd(t *testing.T) {
	s, _ := newTestStore(t)
	s.Names = []string{"Marta", "x"}
	_, err := s.Add(inputFor("k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "findings_denied_names[1]")
	assert.NotContains(t, err.Error(), "Marta")
	assert.NoDirExists(t, s.Dir)
}

func TestAdd_ConfiguredNamesAreRefused(t *testing.T) {
	s, _ := newTestStore(t)
	s.Names = []string{"Marta"}
	in := inputFor("k")
	in.Finding = "Marta said the lock is fine"
	_, err := s.Add(in)
	var rejected *RejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, []Violation{{Column: "finding", Reason: "contains something that looks like person-name"}}, rejected.Violations)
}

// TestAdd_ChangesTheRegistryOnlyThroughPublish: Store never opens the registry
// for writing. A Publish that does nothing leaves the registry exactly as it
// was, absent or present; and the snapshot it is handed is the whole new file.
func TestAdd_ChangesTheRegistryOnlyThroughPublish(t *testing.T) {
	s, _ := newTestStore(t)
	var gotPath string
	var gotData []byte
	calls := 0
	s.Publish = func(path string, data []byte) error {
		calls++
		gotPath, gotData = path, append([]byte(nil), data...)
		return nil
	}

	mustAdd(t, s, inputFor("one"))
	assert.Equal(t, 1, calls)
	assert.Equal(t, s.RegistryPath(), gotPath)
	assert.True(t, strings.HasPrefix(string(gotData), header))
	assert.NoFileExists(t, s.RegistryPath(), "Publish did nothing, so there is no registry: Store wrote none")

	// With a registry on disk, still unchanged.
	require.NoError(t, os.WriteFile(s.RegistryPath(), []byte(header), 0o600))
	mustAdd(t, s, inputFor("two"))
	assert.Equal(t, header, string(readFile(t, s.RegistryPath())))
	assert.Equal(t, 2, calls)
}

func TestAdd_PublishFailureLeavesTheRegistryUntouched(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("one"))
	before := readFile(t, s.RegistryPath())

	boom := errors.New("disk full")
	s.Publish = func(string, []byte) error { return boom }
	_, err := s.Add(inputFor("two"))
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), s.RegistryPath(), "the error names the path")
	assert.Equal(t, before, readFile(t, s.RegistryPath()))

	s.Publish = nil
	mustAdd(t, s, inputFor("three"))
	assert.Equal(t, 3, strings.Count(string(readFile(t, s.RegistryPath())), "\n"), "the lock was released: the next add works")
}

// The Add comment promises the first two steps create nothing and the rest leaves
// the directory behind. The first half is pinned by the refusal tests above; this
// is the second: a lock that stays busy, on a repository that had no .handover/.
func TestAdd_ABusyLockLeavesTheDirectoryItCreated(t *testing.T) {
	s, _ := newTestStore(t)
	s.Sleep = func(time.Duration) {}
	s.LockAttempts = 2
	s.TryLock = func(string) (func() error, error) { return nil, ErrBusy }

	_, err := s.Add(inputFor("k"))
	require.ErrorIs(t, err, ErrBusy)
	assert.DirExists(t, s.Dir, "the directory was created before the lock was tried")
	assert.NoFileExists(t, s.RegistryPath())
}

func TestAdd_BusyLockWritesNothing(t *testing.T) {
	s, _ := newTestStore(t)
	require.NoError(t, os.Mkdir(s.Dir, 0o700))
	held, err := TryLockFile(s.lockPath())
	require.NoError(t, err)

	s.LockAttempts = 3
	_, err = s.Add(inputFor("k"))
	require.ErrorIs(t, err, ErrBusy)
	assert.NoFileExists(t, s.RegistryPath())

	require.NoError(t, held())
	mustAdd(t, s, inputFor("k"))
}

// TestAdd_LockRetryBudgetIsBoundedAndExact pins the budget with injected
// dependencies: LockAttempts tries, LockAttempts-1 waits of LockWait, then ErrBusy.
func TestAdd_LockRetryBudgetIsBoundedAndExact(t *testing.T) {
	s, _ := newTestStore(t)
	var tries int
	var waits []time.Duration
	s.Sleep = func(d time.Duration) { waits = append(waits, d) }
	s.LockAttempts, s.LockWait = 7, 5*time.Millisecond
	s.TryLock = func(string) (func() error, error) { tries++; return nil, ErrBusy }

	_, err := s.Add(inputFor("k"))
	require.ErrorIs(t, err, ErrBusy)
	assert.Equal(t, 7, tries)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}, waits)
	assert.NoFileExists(t, s.RegistryPath())

	// Succeeds on the third try: two waits, and the add goes through.
	tries, waits = 0, nil
	s.TryLock = func(string) (func() error, error) {
		tries++
		if tries < 3 {
			return nil, ErrBusy
		}
		return func() error { return nil }, nil
	}
	mustAdd(t, s, inputFor("k"))
	assert.Equal(t, 3, tries)
	assert.Len(t, waits, 2)
}

func TestAdd_LockRetryDefaultsAre100TriesAnd20msWaits(t *testing.T) {
	s, _ := newTestStore(t)
	var tries int
	var waits []time.Duration
	s.Sleep = func(d time.Duration) { waits = append(waits, d) }
	s.TryLock = func(string) (func() error, error) { tries++; return nil, ErrBusy }

	_, err := s.Add(inputFor("k"))
	require.ErrorIs(t, err, ErrBusy)
	assert.Equal(t, 100, tries)
	require.Len(t, waits, 99)
	assert.Equal(t, 20*time.Millisecond, waits[0])
}

func TestAdd_AnErrorFromTheLockOtherThanBusyStopsAtOnce(t *testing.T) {
	s, _ := newTestStore(t)
	var tries, sleeps int
	boom := errors.New("no such device")
	s.Sleep = func(time.Duration) { sleeps++ }
	s.TryLock = func(string) (func() error, error) { tries++; return nil, boom }

	_, err := s.Add(inputFor("k"))
	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, ErrBusy)
	assert.Equal(t, 1, tries)
	assert.Zero(t, sleeps)
}

// A release that fails after a successful publish must not look like a lost row.
func TestAdd_ReleaseFailureIsReportedAndSaysTheRowWasRecorded(t *testing.T) {
	s, _ := newTestStore(t)
	boom := errors.New("handle is gone")
	s.TryLock = func(string) (func() error, error) { return func() error { return boom }, nil }

	res, err := s.Add(inputFor("k"))
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "WAS recorded")
	assert.Equal(t, "k", res.Row.Chiave, "the result still carries the row")
	assert.Contains(t, string(readFile(t, s.RegistryPath())), "\tk\t")
}

func TestAdd_ReleaseFailureAfterAnotherFailureKeepsBoth(t *testing.T) {
	s, _ := newTestStore(t)
	publishErr, relErr := errors.New("disk full"), errors.New("handle is gone")
	s.TryLock = func(string) (func() error, error) { return func() error { return relErr }, nil }
	s.Publish = func(string, []byte) error { return publishErr }

	_, err := s.Add(inputFor("k"))
	require.ErrorIs(t, err, publishErr)
	require.ErrorIs(t, err, relErr)
}

// goodRegistry returns a registry with one valid row, and the fields of that row.
func goodRegistry(t *testing.T, s *Store) (data []byte, fields []string) {
	t.Helper()
	mustAdd(t, s, inputFor("lock-race"))
	data = readFile(t, s.RegistryPath())
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2)
	return data, strings.Split(lines[1], "\t")
}

// TestRegistry_StructuralProblemsAreRefusedByAddAndStale is D7: a registry that
// is not valid is refused, by both commands, with the line and a fixed reason,
// and it is not touched.
func TestRegistry_StructuralProblemsAreRefusedByAddAndStale(t *testing.T) {
	setup := func(t *testing.T, mutate func(good []byte, fields []string) []byte) (*Store, []byte) {
		s, _ := newTestStore(t)
		good, fields := goodRegistry(t, s)
		bad := mutate(good, append([]string(nil), fields...))
		require.NoError(t, os.WriteFile(s.RegistryPath(), bad, 0o600))
		return s, bad
	}
	rowWith := func(change func(f []string)) func([]byte, []string) []byte {
		return func(_ []byte, f []string) []byte {
			change(f)
			return []byte(header + strings.Join(f, "\t") + "\n")
		}
	}

	cases := []struct {
		name   string
		mutate func(good []byte, fields []string) []byte
		want   string
	}{
		{"last row without a newline", func(good []byte, _ []string) []byte { return good[:len(good)-1] }, "line 2: the last line does not end with a newline"},
		{"header differs", func(good []byte, _ []string) []byte {
			return []byte(strings.Replace(string(good), "ts\tlotto", "ts\tlotti", 1))
		}, "line 1: the header is not the expected one"},
		{"header with a carriage return", func(good []byte, _ []string) []byte {
			return []byte(strings.Replace(string(good), "costo\n", "costo\r\n", 1))
		}, "line 1: the header is not the expected one"},
		{"14 columns", func(_ []byte, f []string) []byte {
			return []byte(header + strings.Join(f[:14], "\t") + "\n")
		}, "line 2: the line has 14 columns, expected 15"},
		{"16 columns (a stray tab)", func(_ []byte, f []string) []byte {
			return []byte(header + strings.Join(append(f, "extra"), "\t") + "\n")
		}, "line 2: the line has 16 columns, expected 15"},
		{"carriage return in a row", rowWith(func(f []string) { f[6] += "\r" }), "line 2: the line contains a carriage return"},
		{"gate outside its list", rowWith(func(f []string) { f[2] = "Piano" }), "line 2: column gate has a value outside its list"},
		{"severita outside its list", rowWith(func(f []string) { f[7] = "major" }), "line 2: column severita has a value outside its list"},
		{"decisione outside its list", rowWith(func(f []string) { f[9] = "ACCEPT" }), "line 2: column decisione has a value outside its list"},
		{"esito outside its list", rowWith(func(f []string) { f[11] = "Aperto" }), "line 2: column esito has a value outside its list"},
		{"chiave not a key", rowWith(func(f []string) { f[4] = "Lock_Race" }), "line 2: column chiave is not a valid key"},
		{"ts not a timestamp", rowWith(func(f []string) { f[0] = "2026-11-08T10:00:00Z" }), "line 2: column ts is not a UTC timestamp with milliseconds"},
		{"first_seen not a timestamp", rowWith(func(f []string) { f[5] = "yesterday" }), "line 2: column first_seen is not a UTC timestamp with milliseconds"},
		{"a blank line", func(good []byte, _ []string) []byte { return []byte(string(good) + "\n") }, "line 3: the line has 1 columns, expected 15"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, bad := setup(t, tc.mutate)

			_, err := s.Add(inputFor("another"))
			require.Error(t, err)
			assert.Equal(t, "findings: "+s.RegistryPath()+": "+tc.want, err.Error())
			assert.Equal(t, bad, readFile(t, s.RegistryPath()), "a registry that is refused is not touched")

			_, err = s.Stale(7)
			require.Error(t, err)
			assert.Equal(t, "findings: "+s.RegistryPath()+": "+tc.want, err.Error())
		})
	}
}

func TestRegistry_EmptyFileIsAnEmptyRegistry(t *testing.T) {
	s, _ := newTestStore(t)
	require.NoError(t, os.Mkdir(s.Dir, 0o700))
	require.NoError(t, os.WriteFile(s.RegistryPath(), nil, 0o600))

	mustAdd(t, s, inputFor("k"))
	assert.True(t, strings.HasPrefix(string(readFile(t, s.RegistryPath())), header), "the header is written once, by the first add")
}

func TestRegistry_RulesOfContentAreNotRerunOnRead(t *testing.T) {
	s, _ := newTestStore(t)
	row := newRow(inputFor("old"), "2026-10-01T00:00:00.000Z", "2026-10-01T00:00:00.000Z")
	row.Finding = "password=" + "hunter22 written before the rule existed"
	require.NoError(t, os.Mkdir(s.Dir, 0o700))
	require.NoError(t, os.WriteFile(s.RegistryPath(), []byte(header+row.line()), 0o600))

	mustAdd(t, s, inputFor("new"))
	_, err := s.Stale(7)
	require.NoError(t, err)
}

func TestAdd_RefusesARegistryOverTheCapAndLeavesItAlone(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("one"))
	for len(readFile(t, s.RegistryPath())) <= 600 {
		mustAdd(t, s, inputFor("again"))
	}
	s.MaxBytes = 600
	before := readFile(t, s.RegistryPath())

	_, err := s.Add(inputFor("k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the registry is larger than 600 bytes, refusing to read it")
	_, err = s.Stale(7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the registry is larger than 600 bytes, refusing to read it")
	assert.Equal(t, before, readFile(t, s.RegistryPath()))
}

func TestAdd_RefusesARegistryOver16MiB(t *testing.T) {
	s, _ := newTestStore(t)
	require.NoError(t, os.Mkdir(s.Dir, 0o700))
	data := bigRegistry(DefaultMaxBytes + 1)
	require.NoError(t, os.WriteFile(s.RegistryPath(), data, 0o600))

	_, err := s.Add(inputFor("k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the registry is larger than 16777216 bytes, refusing to read it")
	_, err = s.Stale(7)
	require.Error(t, err)
	assert.Equal(t, int64(len(data)), fileSize(t, s.RegistryPath()), "left as it was")
}

// bigRegistry returns a structurally valid registry of exactly size bytes: a
// header and one row whose finding is as long as it needs to be. parseRegistry
// checks structure, not the lengths that Validate applies at write time.
func bigRegistry(size int64) []byte {
	row := newRow(inputFor("big"), "2026-10-01T00:00:00.000Z", "2026-10-01T00:00:00.000Z")
	row.Finding = ""
	pad := int(size) - len(header) - len(row.line())
	row.Finding = strings.Repeat("a", pad)
	return []byte(header + row.line())
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}

// TestAdd_SnapshotAtTheCapIsAcceptedAndOneByteMoreIsRefused is D7 (2): the cap
// also applies to the snapshot about to be published, so an add that succeeds
// can never produce a registry the next command refuses.
func TestAdd_SnapshotAtTheCapIsAcceptedAndOneByteMoreIsRefused(t *testing.T) {
	probe, _ := newTestStore(t)
	mustAdd(t, probe, inputFor("k1"))
	first := int64(len(readFile(t, probe.RegistryPath())))
	secondLine := int64(len(newRow(inputFor("k2"), formatTimestamp(t0), formatTimestamp(t0)).line()))

	s, _ := newTestStore(t)
	s.MaxBytes = first + secondLine - 1
	mustAdd(t, s, inputFor("k1"))
	before := readFile(t, s.RegistryPath())

	_, err := s.Add(inputFor("k2"))
	require.Error(t, err, "one byte over the cap is refused")
	assert.Equal(t, "findings: "+s.RegistryPath()+": the registry would exceed "+itoa(s.MaxBytes)+" bytes, nothing was written", err.Error())
	assert.Equal(t, before, readFile(t, s.RegistryPath()))
	assert.Equal(t, []string{".gitignore", "findings.lock", "findings.tsv"}, dirNames(t, s.Dir), "no temporary file")
	_, err = s.Stale(7)
	require.NoError(t, err, "and the registry that is left can still be read")

	s.MaxBytes = first + secondLine
	mustAdd(t, s, inputFor("k2"))
	assert.Equal(t, s.MaxBytes, int64(len(readFile(t, s.RegistryPath()))), "exactly at the cap is accepted")
	_, err = s.Stale(7)
	require.NoError(t, err, "and a registry exactly at the cap is readable")
}

func TestAdd_TheFirstRowThatCrossesTheCapIsRefused(t *testing.T) {
	line := int64(len(newRow(inputFor("k1"), formatTimestamp(t0), formatTimestamp(t0)).line()))
	s, _ := newTestStore(t)
	s.MaxBytes = int64(len(header)) + 3*line

	for _, key := range []string{"k1", "k2", "k3"} {
		mustAdd(t, s, inputFor(key))
	}
	before := readFile(t, s.RegistryPath())
	_, err := s.Add(inputFor("k4"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would exceed")
	assert.Equal(t, before, readFile(t, s.RegistryPath()))
	_, err = s.Stale(7)
	require.NoError(t, err)
}

// At the real cap: a registry built to the byte, so the add lands exactly on 16 MiB.
func TestAdd_AtTheRealCapOfSixteenMiB(t *testing.T) {
	s, _ := newTestStore(t)
	require.NoError(t, os.Mkdir(s.Dir, 0o700))
	newLine := int64(len(newRow(inputFor("k"), formatTimestamp(t0), formatTimestamp(t0)).line()))

	data := bigRegistry(DefaultMaxBytes - newLine)
	require.NoError(t, os.WriteFile(s.RegistryPath(), data, 0o600))
	mustAdd(t, s, inputFor("k"))
	assert.Equal(t, DefaultMaxBytes, fileSize(t, s.RegistryPath()))

	require.NoError(t, os.WriteFile(s.RegistryPath(), bigRegistry(DefaultMaxBytes-newLine+1), 0o600))
	_, err := s.Add(inputFor("k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the registry would exceed 16777216 bytes, nothing was written")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
