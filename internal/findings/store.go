package findings

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	cabfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

const (
	// DefaultMaxBytes caps the registry: the existing file before it is read, and
	// the snapshot before it is published. Equal passes, one byte more is refused.
	DefaultMaxBytes int64 = 16 << 20

	// MaxStaleDays keeps `days * 24h` inside time.Duration (about 292 years): a
	// larger value would wrap negative and make every finding "stale".
	MaxStaleDays = 36500

	// The retry budget of `add` when the lock is busy: lockAttempts tries
	// separated by lockAttempts-1 waits of lockWait, so a BUDGET OF REQUESTED
	// WAITS of 1.98 s, not a maximum duration (syscalls, the owner check and
	// scheduling add to it, as in rename_windows.go). Tuned on a Mac: Windows may
	// need more, and these two constants are where to raise it.
	lockAttempts = 100
	lockWait     = 20 * time.Millisecond

	registryName  = "findings.tsv"
	lockName      = "findings.lock"
	gitignoreName = ".gitignore"
	gitignoreBody = "*\n"
	dirMode       = 0o700
	fileMode      = 0o600
)

// TryLockFunc, ErrBusy and TryLockFile are in lock.go.

// PublishFunc replaces the registry with the given snapshot. It is the ONLY
// place the content of findings.tsv changes: Store never opens the registry for
// writing. The default is one atomic rename (fs.AtomicWriteBytes), so a crash at
// any point leaves the registry as it was or as it becomes, never a prefix.
type PublishFunc func(path string, data []byte) error

func defaultPublish(path string, data []byte) error {
	return cabfs.AtomicWriteBytes(path, data, fileMode)
}

// Store is the registry of one repository. Dir is its .handover directory. The
// zero value of every other field means "the default"; tests inject the rest.
type Store struct {
	Dir          string
	Names        []string // findings_denied_names; empty = names are not checked
	Now          func() time.Time
	TryLock      TryLockFunc
	Publish      PublishFunc
	LockAttempts int
	LockWait     time.Duration
	Sleep        func(time.Duration)
	MaxBytes     int64
}

// AddResult is what a successful Add recorded. Notes are informative lines for
// stderr, without the `findings: note: ` prefix (the command owns that format).
type AddResult struct {
	Row   Row
	Notes []string
}

// StaleResult is what Stale found: the current row of every key that is still
// open past the threshold, oldest first_seen first.
type StaleResult struct {
	Rows  []Row
	Notes []string
}

// TSV renders the header followed by the rows, byte for byte as the registry
// stores them: what `stale` prints on stdout, readable by a machine.
func (r StaleResult) TSV() string {
	var b strings.Builder
	b.WriteString(header)
	for _, row := range r.Rows {
		b.WriteString(row.line())
	}
	return b.String()
}

// RegistryPath is where the rows live.
func (s *Store) RegistryPath() string { return filepath.Join(s.Dir, registryName) }

func (s *Store) lockPath() string { return filepath.Join(s.Dir, lockName) }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultMaxBytes
}

// Add records one finding. Order (D17): validate (pure) -> directory -> lock ->
// clock -> read and check the registry -> first_seen -> snapshot -> publish ->
// release. A refusal before the lock creates nothing, not even the directory.
func (s *Store) Add(in Input) (res AddResult, err error) {
	if err := CheckNames(s.Names); err != nil {
		return AddResult{}, err
	}
	if violations := Validate(in, s.Names); len(violations) > 0 {
		return AddResult{}, &RejectedError{Violations: violations}
	}
	if err := s.ensureDir(); err != nil {
		return AddResult{}, err
	}
	release, err := s.acquire()
	if err != nil {
		return AddResult{}, err
	}
	defer func() { res, err = finishRelease(res, err, release) }()
	return s.addLocked(in)
}

// finishRelease releases the lock and keeps its failure visible. If the row was
// recorded, the error says so: a lock that did not release is not a lost row.
func finishRelease(res AddResult, err error, release func() error) (AddResult, error) {
	relErr := release()
	switch {
	case relErr == nil:
		return res, err
	case err == nil:
		return res, fmt.Errorf("findings: the row WAS recorded, but releasing the lock failed: %w", relErr)
	default:
		return res, errors.Join(err, relErr)
	}
}

func (s *Store) addLocked(in Input) (AddResult, error) {
	ts := formatTimestamp(s.now())
	path := s.RegistryPath()

	reg, err := loadRegistry(path, s.maxBytes())
	if err != nil {
		return AddResult{}, err
	}
	row := newRow(in, ts, firstSeenFor(reg.rows, in.Chiave, ts))
	snapshot, err := buildSnapshot(path, reg.data, row, s.maxBytes())
	if err != nil {
		return AddResult{}, err
	}
	if err := s.publish(path, snapshot); err != nil {
		return AddResult{}, fmt.Errorf("findings: publishing %s (the registry was not changed): %w", path, err)
	}

	var notes []string
	if reg.looseMode != 0 {
		notes = append(notes, fmt.Sprintf("the registry had mode %04o, it is 0600 now", reg.looseMode))
	}
	return AddResult{Row: row, Notes: notes}, nil
}

func (s *Store) publish(path string, data []byte) error {
	if s.Publish != nil {
		return s.Publish(path, data)
	}
	return defaultPublish(path, data)
}

// firstSeenFor is B2.5: the first row with the same key wins, else this row's ts.
func firstSeenFor(rows []Row, chiave, ts string) string {
	for _, r := range rows {
		if r.Chiave == chiave {
			return r.FirstSeen
		}
	}
	return ts
}

func buildSnapshot(path string, existing []byte, row Row, maxBytes int64) ([]byte, error) {
	line := row.line()
	withHeader := len(existing) == 0
	size := len(existing) + len(line)
	if withHeader {
		size += len(header)
	}
	if int64(size) > maxBytes {
		return nil, fmt.Errorf("findings: %s: the registry would exceed %d bytes, nothing was written", path, maxBytes)
	}
	snapshot := make([]byte, 0, size)
	if withHeader {
		snapshot = append(snapshot, header...)
	}
	snapshot = append(snapshot, existing...)
	return append(snapshot, line...), nil
}

// ensureDir is D13. If the directory is missing it is created 0700 (Mkdir, not
// MkdirAll: the repository root exists) and ONLY its creator writes the
// .gitignore. If it exists it is checked, never changed.
//
// Declared window: between Mkdir and the .gitignore the directory exists without
// it. A crash there leaves it without, and nothing writes it later.
func (s *Store) ensureDir() error {
	info, err := os.Lstat(s.Dir)
	if err == nil {
		return checkDir(s.Dir, info)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("findings: %s: %w", s.Dir, err)
	}
	err = os.Mkdir(s.Dir, dirMode)
	if errors.Is(err, fs.ErrExist) {
		// Lost the creation race: it is somebody else's directory now, so it is
		// checked like any existing one and this process writes no .gitignore.
		return s.checkExistingDir()
	}
	if err != nil {
		return fmt.Errorf("findings: creating %s: %w", s.Dir, err)
	}
	return s.finishNewDir()
}

func (s *Store) checkExistingDir() error {
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return fmt.Errorf("findings: %s: %w", s.Dir, err)
	}
	return checkDir(s.Dir, info)
}

// checkDirForRead is what Stale does where Add calls ensureDir: the same checks
// (checkDir) and no creation. A missing directory is not an error, it means
// there is no registry yet. Without this, Stale opened the registry by its path
// and followed a `.handover` that was a link to a registry somewhere else, and a
// broken link read as "no registry".
func (s *Store) checkDirForRead() (exists bool, err error) {
	info, err := os.Lstat(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("findings: %s: %w", s.Dir, err)
	}
	if err := checkDir(s.Dir, info); err != nil {
		return false, err
	}
	return true, nil
}

// finishNewDir pins the mode of a directory this process just created and writes
// its .gitignore. If either fails the empty directory is removed, so that a
// retry creates it again and the .gitignore is not skipped for good.
func (s *Store) finishNewDir() error {
	err := os.Chmod(s.Dir, dirMode)
	if err == nil {
		_, err = cabfs.WriteIfAbsentBytes(filepath.Join(s.Dir, gitignoreName), []byte(gitignoreBody), fileMode)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("findings: preparing %s: %w", s.Dir, err), os.Remove(s.Dir))
	}
	return nil
}

// checkDir is what both commands require of the directory: not a link (Lstat
// does not follow, so a broken link is a link too), a directory, ours.
func checkDir(dir string, info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("findings: %s: is a symbolic link, refusing to follow it", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("findings: %s: exists and is not a directory", dir)
	}
	if err := security.CheckOwnedInfo(dir, info); err != nil {
		return fmt.Errorf("findings: %s: %w", dir, err)
	}
	return nil
}

// acquire takes the lock: LockAttempts tries and LockAttempts-1 waits of
// LockWait between them, then ErrBusy. Any other error from the lock stops it.
func (s *Store) acquire() (func() error, error) {
	attempts, wait := s.LockAttempts, s.LockWait
	if attempts <= 0 {
		attempts = lockAttempts
	}
	if wait <= 0 {
		wait = lockWait
	}
	tryLock, sleep := s.TryLock, s.Sleep
	if tryLock == nil {
		tryLock = TryLockFile
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		release, err := tryLock(s.lockPath())
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, ErrBusy) {
			return nil, err
		}
		if attempt < attempts {
			sleep(wait)
		}
	}
	return nil, ErrBusy
}

// registry is a registry as read from disk.
type registry struct {
	exists    bool
	data      []byte
	rows      []Row
	looseMode fs.FileMode // the permission bits when group or other had access; 0 otherwise
}

// loadRegistry reads and validates the registry at path; an absent one is empty.
// The size is checked before reading (ReadOwnedFile has no limit of its own) and
// again on what was read; the Lstat bounds what is read, absent a hand edit
// racing it.
func loadRegistry(path string, maxBytes int64) (registry, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return registry{}, nil
	}
	if err != nil {
		return registry{}, fmt.Errorf("findings: %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return registry{}, fmt.Errorf("findings: %s: is a symbolic link, refusing to follow it", path)
	}
	if !info.Mode().IsRegular() {
		return registry{}, fmt.Errorf("findings: %s: is not a regular file", path)
	}
	if info.Size() > maxBytes {
		return registry{}, tooLargeError(path, maxBytes)
	}

	data, err := security.ReadOwnedFile(path)
	if err != nil {
		return registry{}, fmt.Errorf("findings: %s: %w", path, err)
	}
	if int64(len(data)) > maxBytes {
		return registry{}, tooLargeError(path, maxBytes)
	}
	rows, err := parseRegistry(data)
	if err != nil {
		return registry{}, fmt.Errorf("findings: %s: %w", path, err)
	}

	reg := registry{exists: true, data: data, rows: rows}
	if security.DirPermsAreLoose(info.Mode().Perm()) {
		reg.looseMode = info.Mode().Perm()
	}
	return reg, nil
}

func tooLargeError(path string, maxBytes int64) error {
	return fmt.Errorf("findings: %s: the registry is larger than %d bytes, refusing to read it", path, maxBytes)
}

// Stale lists the findings still open after more than days days. It never takes
// the lock and never writes anything (not the registry, not the lock file, not
// the directory): it reads one whole snapshot, which is either the registry
// before a concurrent add or the one after it, because add replaces the file
// with a rename.
func (s *Store) Stale(days int) (StaleResult, error) {
	if days < 1 || days > MaxStaleDays {
		return StaleResult{}, fmt.Errorf("findings: days must be between 1 and %d", MaxStaleDays)
	}
	path := s.RegistryPath()
	dirExists, err := s.checkDirForRead()
	if err != nil {
		return StaleResult{}, err
	}
	if !dirExists {
		return StaleResult{Notes: []string{"no registry yet at " + path}}, nil
	}
	reg, err := loadRegistry(path, s.maxBytes())
	if err != nil {
		return StaleResult{}, err
	}
	if !reg.exists {
		return StaleResult{Notes: []string{"no registry yet at " + path}}, nil
	}

	rows, err := staleRows(reg.rows, s.now(), days)
	if err != nil {
		return StaleResult{}, fmt.Errorf("findings: %s: %w", path, err)
	}
	notes := []string{fmt.Sprintf("%d open for more than %d days", len(rows), days)}
	if reg.looseMode != 0 {
		notes = append(notes, fmt.Sprintf("the registry has mode %04o (the next add makes it 0600)", reg.looseMode))
	}
	return StaleResult{Rows: rows, Notes: notes}, nil
}

// staleRows keeps the current row (the last in file order) of every key whose
// esito is open and whose first_seen is strictly older than days days.
//
// parseRegistry already validated every first_seen, so the error below cannot
// happen for a registry that came from loadRegistry; it is returned, not
// swallowed, so that a future change to the validation cannot turn into
// findings silently missing from the list.
func staleRows(rows []Row, now time.Time, days int) ([]Row, error) {
	current := make(map[string]Row, len(rows))
	for _, r := range rows {
		current[r.Chiave] = r
	}
	limit := time.Duration(days) * 24 * time.Hour
	var out []Row
	for _, r := range current {
		if r.Esito != outcomeOpen {
			continue
		}
		firstSeen, err := parseTimestamp(r.FirstSeen)
		if err != nil {
			return nil, fmt.Errorf("the first_seen of key %s is not a timestamp: %w", r.Chiave, err)
		}
		if now.Sub(firstSeen) > limit {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FirstSeen != out[j].FirstSeen {
			return out[i].FirstSeen < out[j].FirstSeen
		}
		return out[i].Chiave < out[j].Chiave
	})
	return out, nil
}
