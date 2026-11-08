package findings

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const day = 24 * time.Hour

// addAt records a finding as if the clock read t0+offset.
func addAt(t *testing.T, s *Store, clock *fakeClock, offset time.Duration, chiave, esito string) {
	t.Helper()
	clock.now = t0.Add(offset)
	in := inputFor(chiave)
	in.Esito = esito
	mustAdd(t, s, in)
}

func staleKeys(res StaleResult) []string {
	keys := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		keys = append(keys, r.Chiave)
	}
	return keys
}

// TestStale_OpenAndOlderThanNDays is the boundary table: strictly older than N
// days, only the CURRENT row of a key counts, and only if it is still open.
func TestStale_OpenAndOlderThanNDays(t *testing.T) {
	s, clock := newTestStore(t)
	addAt(t, s, clock, 0, "exactly-n", "aperto")                  // first_seen = t0
	addAt(t, s, clock, -time.Millisecond, "n-and-a-ms", "aperto") // one millisecond older than exactly-n
	addAt(t, s, clock, -day, "older", "aperto")
	addAt(t, s, clock, -30*day, "closed-last", "aperto")
	addAt(t, s, clock, -29*day, "closed-last", "chiuso")
	addAt(t, s, clock, -30*day, "rejected-last", "aperto")
	addAt(t, s, clock, -29*day, "rejected-last", "respinto")
	addAt(t, s, clock, -20*day, "closed-then-reopened", "chiuso")
	addAt(t, s, clock, -19*day, "closed-then-reopened", "aperto")
	addAt(t, s, clock, -30*day, "fresh-key-twice", "aperto")
	addAt(t, s, clock, 0, "fresh-key-twice", "aperto")
	addAt(t, s, clock, 0, "young", "aperto")

	// "Now" is t0 + 7 days. exactly-n has first_seen t0: exactly 7 days old, not stale.
	// n-and-a-ms has first_seen t0-1ms: 7 days and 1 ms old, stale. older: 8 days.
	clock.now = t0.Add(7 * day)
	res, err := s.Stale(7)
	require.NoError(t, err)
	// first_seen ascending: fresh-key-twice (t0-30d) and closed-then-reopened
	// (t0-20d) carry the first_seen of their FIRST row, older is t0-1d,
	// n-and-a-ms is t0-1ms.
	assert.Equal(t, []string{"fresh-key-twice", "closed-then-reopened", "older", "n-and-a-ms"}, staleKeys(res))
}

func TestStale_OneRowPerKeyIsTheLastOneInFileOrder(t *testing.T) {
	s, clock := newTestStore(t)
	addAt(t, s, clock, -10*day, "k", "aperto")
	addAt(t, s, clock, -9*day, "k", "aperto")
	clock.now = t0
	res, err := s.Stale(7)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, formatTimestamp(t0.Add(-9*day)), res.Rows[0].Ts, "the current row, not the first")
	assert.Equal(t, formatTimestamp(t0.Add(-10*day)), res.Rows[0].FirstSeen)
}

func TestStale_OrderIsFirstSeenThenKey(t *testing.T) {
	s, clock := newTestStore(t)
	addAt(t, s, clock, -20*day, "zeta", "aperto")
	addAt(t, s, clock, -30*day, "mid", "aperto")
	addAt(t, s, clock, -30*day, "alpha", "aperto")
	clock.now = t0
	res, err := s.Stale(7)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, staleKeys(res), "oldest first; same instant by key")
}

func TestStale_SummaryNoteCountsWhatItListed(t *testing.T) {
	s, clock := newTestStore(t)
	addAt(t, s, clock, -10*day, "one", "aperto")
	addAt(t, s, clock, -10*day, "two", "aperto")
	clock.now = t0
	res, err := s.Stale(7)
	require.NoError(t, err)
	assert.Equal(t, []string{"2 open for more than 7 days"}, res.Notes)

	res, err = s.Stale(30)
	require.NoError(t, err)
	assert.Empty(t, res.Rows)
	assert.Equal(t, []string{"0 open for more than 30 days"}, res.Notes)
}

func TestStale_NoRegistryReturnsOnlyANoteAndCreatesNothing(t *testing.T) {
	s, _ := newTestStore(t)
	res, err := s.Stale(7)
	require.NoError(t, err)
	assert.Empty(t, res.Rows)
	assert.Equal(t, []string{"no registry yet at " + s.RegistryPath()}, res.Notes)
	assert.NoDirExists(t, s.Dir, "stale never creates the directory")

	// The directory without a registry: same answer, still nothing created.
	require.NoError(t, os.Mkdir(s.Dir, 0o700))
	res, err = s.Stale(7)
	require.NoError(t, err)
	assert.Equal(t, []string{"no registry yet at " + s.RegistryPath()}, res.Notes)
	assert.Empty(t, dirNames(t, s.Dir))
}

func TestStale_DaysOutOfRangeIsAnError(t *testing.T) {
	s, _ := newTestStore(t)
	for _, days := range []int{0, -1, MaxStaleDays + 1, 1 << 40} {
		_, err := s.Stale(days)
		require.Error(t, err, "days=%d", days)
		assert.Contains(t, err.Error(), "days must be between 1 and 36500")
	}
	_, err := s.Stale(MaxStaleDays)
	require.NoError(t, err, "the largest accepted value still fits a time.Duration")
}

// A huge day count must not wrap into a negative threshold that lists everything.
func TestStale_TheLargestThresholdListsNothingRecent(t *testing.T) {
	s, clock := newTestStore(t)
	addAt(t, s, clock, -day, "yesterday", "aperto")
	clock.now = t0
	res, err := s.Stale(MaxStaleDays)
	require.NoError(t, err)
	assert.Empty(t, res.Rows)
}

// Stale does not take the lock: a busy lock does not stop it.
func TestStale_DoesNotTakeTheLock(t *testing.T) {
	s, clock := newTestStore(t)
	addAt(t, s, clock, -10*day, "k", "aperto")
	held, err := TryLockFile(s.lockPath())
	require.NoError(t, err)
	defer func() { require.NoError(t, held()) }()

	s.TryLock = func(string) (func() error, error) {
		require.Fail(t, "Stale must not take the lock")
		return nil, nil
	}
	clock.now = t0
	res, err := s.Stale(7)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, staleKeys(res))
}

// TestStale_AddedColumnsAreReturnedWhole: Stale hands back the rows as stored,
// every column, so the command can print them unchanged.
func TestStale_RowsAreReturnedAsStored(t *testing.T) {
	s, clock := newTestStore(t)
	in := inputFor("k")
	in.Azione = "do the thing"
	in.Token = "5000"
	clock.now = t0.Add(-10 * day)
	mustAdd(t, s, in)
	clock.now = t0
	res, err := s.Stale(7)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	stored := strings.Split(strings.TrimSuffix(string(readFile(t, s.RegistryPath())), "\n"), "\n")[1]
	assert.Equal(t, stored+"\n", res.Rows[0].line())
}

// Diff gate round 1 (CRI M3). A key's age is counted from its FIRST row, the
// definition Add uses to write first_seen; its state is the LAST row's. Add keeps
// the column equal across the rows of a key, so the two only differ in a registry
// edited by hand, which the reader accepts. Each key below crosses the 7-day
// threshold the other way round under the old rule (the last row's date).
func TestStale_AgeComesFromTheFirstRowOfTheKeyAndStateFromTheLast(t *testing.T) {
	s, _ := newTestStore(t) // now = t0
	at := func(d time.Duration) string { return formatTimestamp(t0.Add(d)) }
	row := func(chiave, esito, firstSeen, finding string) string {
		in := inputFor(chiave)
		in.Esito, in.Finding = esito, finding
		return newRow(in, at(-time.Hour), firstSeen).line()
	}
	registry := header +
		// born 10 days ago, its last row says 1: listed (the old rule: not listed)
		row("born-old", "aperto", at(-10*day), "first") + row("born-old", "aperto", at(-day), "last") +
		// born 1 day ago, its last row says 30: not listed (the old rule: listed)
		row("born-young", "aperto", at(-day), "first") + row("born-young", "aperto", at(-30*day), "last") +
		// born old but closed by its last row: not listed
		row("closed-last", "aperto", at(-10*day), "first") + row("closed-last", "chiuso", at(-day), "last") +
		// born 9 days ago, its last row says 40: listed AFTER born-old (the old order: before)
		row("born-later", "aperto", at(-9*day), "first") + row("born-later", "aperto", at(-40*day), "last")
	require.NoError(t, os.MkdirAll(s.Dir, 0o700))
	require.NoError(t, os.WriteFile(s.RegistryPath(), []byte(registry), 0o600))

	res, err := s.Stale(7)
	require.NoError(t, err)

	assert.Equal(t, []string{"born-old", "born-later"}, staleKeys(res))
	require.Len(t, res.Rows, 2)
	assert.Equal(t, at(-10*day), res.Rows[0].FirstSeen, "the row carries the date of the key's first row")
	assert.Equal(t, at(-9*day), res.Rows[1].FirstSeen)
	assert.Equal(t, "last", res.Rows[0].Finding, "and everything else comes from the last row")
	assert.Equal(t, "last", res.Rows[1].Finding)
}
