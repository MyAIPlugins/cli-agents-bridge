//go:build !windows

package findings

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}

// The cap is checked on the size Lstat reports, BEFORE the file is read: that is
// what keeps a huge registry out of memory. A registry over the cap that cannot
// even be opened tells them apart: refused for its size, not for its permissions.
// Without the early check the read comes first and the error is "permission denied".
func TestAdd_OversizedRegistryIsRefusedBeforeItIsRead(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root opens files whatever their mode: the premise of this test does not hold")
	}
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("k"))
	path := s.RegistryPath()
	before := readFile(t, path)
	s.MaxBytes = int64(len(before)) - 1

	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	_, openErr := os.Open(path)
	require.Error(t, openErr, "premise: the registry cannot be opened")

	_, err := s.Add(inputFor("k2"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is larger than")
	assert.NotContains(t, err.Error(), "permission denied", "the size was judged before any read was attempted")

	require.NoError(t, os.Chmod(path, 0o600))
	assert.Equal(t, before, readFile(t, path), "a refused add leaves the registry as it was")
}

// BAR 9, creation: the directory is 0700, the registry and the lock 0600, and
// the .gitignore `*` keeps the folder out of the repository.
func TestAdd_CreatesTheDirectoryAndFilesWithTheRightModes(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("k"))

	assert.Equal(t, os.FileMode(0o700), modeOf(t, s.Dir))
	assert.Equal(t, os.FileMode(0o600), modeOf(t, s.RegistryPath()))
	assert.Equal(t, os.FileMode(0o600), modeOf(t, s.lockPath()))
	assert.Equal(t, os.FileMode(0o600), modeOf(t, filepath.Join(s.Dir, ".gitignore")))
	assert.Equal(t, "*\n", string(readFile(t, filepath.Join(s.Dir, ".gitignore"))))
}

func TestAdd_ExistingDirectoryIsNotTouched(t *testing.T) {
	s, _ := newTestStore(t)
	require.NoError(t, os.Mkdir(s.Dir, 0o755))
	require.NoError(t, os.Chmod(s.Dir, 0o755))

	mustAdd(t, s, inputFor("k"))
	assert.Equal(t, os.FileMode(0o755), modeOf(t, s.Dir), "an existing directory keeps its mode (declared deviation from SC-2)")
	assert.NoFileExists(t, filepath.Join(s.Dir, ".gitignore"), "only the creator of the directory writes the .gitignore")
}

func TestAdd_TheGitignoreIsWrittenOnceByTheCreatorAndNeverRewritten(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("one"))
	ignore := filepath.Join(s.Dir, ".gitignore")
	require.NoError(t, os.WriteFile(ignore, []byte("*\n!keep\n"), 0o600))

	mustAdd(t, s, inputFor("two"))
	assert.Equal(t, "*\n!keep\n", string(readFile(t, ignore)))
}

// D16: the published file is always 0600, even when it replaces a 0644 one, and
// that is said on stderr. A refused add changes neither the bytes nor the mode.
func TestAdd_RepublishesALooseRegistryAs0600AndSaysSo(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("one"))
	require.NoError(t, os.Chmod(s.RegistryPath(), 0o644))
	before := readFile(t, s.RegistryPath())

	bad := inputFor("two")
	bad.Finding = "password=" + "hunter22"
	_, err := s.Add(bad)
	require.Error(t, err)
	assert.Equal(t, os.FileMode(0o644), modeOf(t, s.RegistryPath()), "a refused add does not change the mode")
	assert.Equal(t, before, readFile(t, s.RegistryPath()))

	res := mustAdd(t, s, inputFor("two"))
	assert.Equal(t, os.FileMode(0o600), modeOf(t, s.RegistryPath()))
	assert.Equal(t, []string{"the registry had mode 0644, it is 0600 now"}, res.Notes)
	assert.True(t, len(readFile(t, s.RegistryPath())) > len(before))
	assert.Equal(t, string(before), string(readFile(t, s.RegistryPath()))[:len(before)], "the old rows are identical byte for byte")

	res = mustAdd(t, s, inputFor("three"))
	assert.Empty(t, res.Notes, "once it is 0600 there is nothing to say")
}

func TestStale_NotesALooseRegistryAndDoesNotChangeIt(t *testing.T) {
	s, clock := newTestStore(t)
	addAt(t, s, clock, -10*day, "k", "open")
	require.NoError(t, os.Chmod(s.RegistryPath(), 0o644))
	clock.now = t0

	res, err := s.Stale(7)
	require.NoError(t, err)
	assert.Equal(t, []string{"1 open for more than 7 days", "the registry has mode 0644 (the next add makes it 0600)"}, res.Notes)
	assert.Equal(t, os.FileMode(0o644), modeOf(t, s.RegistryPath()))
}

// BAR 7 / D10: stale only reads. With the directory read-only it still works,
// and afterwards nothing is new: no file, not even the lock file, and the
// registry has the same bytes and the same modification time.
func TestStale_NeverWrites(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions: the premise of this test does not hold")
	}
	s, clock := newTestStore(t)
	addAt(t, s, clock, -10*day, "k", "open")
	require.NoError(t, os.Remove(s.lockPath()), "so that a lock file appearing again would be visible")
	require.NoError(t, os.Chmod(s.Dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(s.Dir, 0o700) })

	// The premise, asserted: this directory really refuses a new file.
	probe := filepath.Join(s.Dir, "probe")
	require.Error(t, os.WriteFile(probe, nil, 0o600), "the directory must be read-only for this test to prove anything")

	namesBefore := dirNames(t, s.Dir)
	bytesBefore := readFile(t, s.RegistryPath())
	infoBefore, err := os.Stat(s.RegistryPath())
	require.NoError(t, err)

	clock.now = t0.Add(time.Hour)
	res, err := s.Stale(7)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, staleKeys(res))

	assert.Equal(t, namesBefore, dirNames(t, s.Dir), "no new file")
	assert.NoFileExists(t, s.lockPath())
	assert.Equal(t, bytesBefore, readFile(t, s.RegistryPath()))
	infoAfter, err := os.Stat(s.RegistryPath())
	require.NoError(t, err)
	assert.True(t, infoBefore.ModTime().Equal(infoAfter.ModTime()))
}

// The failure of a REAL publish (not an injected one): with the lock file already
// there and the directory read-only, CreateTemp cannot create the temporary file.
func TestAdd_RealPublishFailureLeavesTheRegistryUntouched(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions: the premise of this test does not hold")
	}
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("one"))
	before := readFile(t, s.RegistryPath())
	require.NoError(t, os.Chmod(s.Dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(s.Dir, 0o700) })
	require.Error(t, os.WriteFile(filepath.Join(s.Dir, "probe"), nil, 0o600), "premise: the directory refuses a new file")

	_, err := s.Add(inputFor("two"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), s.RegistryPath())
	assert.Equal(t, before, readFile(t, s.RegistryPath()))
	assert.Equal(t, []string{".gitignore", "findings.lock", "findings.tsv"}, dirNames(t, s.Dir), "no temporary file left behind")

	require.NoError(t, os.Chmod(s.Dir, 0o700))
	mustAdd(t, s, inputFor("two"))
}

// Links are refused wherever they stand: the directory, the registry, the lock.
func TestAdd_RefusesALinkInPlaceOfTheDirectoryTheRegistryOrTheLock(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "other-file")
	require.NoError(t, os.WriteFile(secret, []byte("untouched"), 0o600))

	t.Run("directory as a valid link", func(t *testing.T) {
		s, _ := newTestStore(t)
		require.NoError(t, os.Symlink(outside, s.Dir))
		_, err := s.Add(inputFor("k"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symbolic link")
		assert.Equal(t, []string{"other-file"}, dirNames(t, outside), "nothing was written through the link")
	})
	t.Run("directory as a broken link", func(t *testing.T) {
		s, _ := newTestStore(t)
		require.NoError(t, os.Symlink(filepath.Join(outside, "missing"), s.Dir))
		_, err := s.Add(inputFor("k"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symbolic link")
		assert.NoFileExists(t, filepath.Join(outside, "missing"))
	})
	t.Run("directory as a regular file", func(t *testing.T) {
		s, _ := newTestStore(t)
		require.NoError(t, os.WriteFile(s.Dir, []byte("x"), 0o600))
		_, err := s.Add(inputFor("k"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})
	t.Run("registry as a link", func(t *testing.T) {
		s, _ := newTestStore(t)
		require.NoError(t, os.Mkdir(s.Dir, 0o700))
		require.NoError(t, os.Symlink(secret, s.RegistryPath()))
		_, err := s.Add(inputFor("k"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symbolic link")
		_, err = s.Stale(7)
		require.Error(t, err)
		assert.Equal(t, "untouched", string(readFile(t, secret)))
	})
	t.Run("lock file as a link", func(t *testing.T) {
		s, _ := newTestStore(t)
		require.NoError(t, os.Mkdir(s.Dir, 0o700))
		require.NoError(t, os.Symlink(secret, s.lockPath()))
		_, err := s.Add(inputFor("k"))
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrBusy)
		assert.NoFileExists(t, s.RegistryPath())
		assert.Equal(t, "untouched", string(readFile(t, secret)))
	})
}

// Diff gate round 1 (CRI B2). Stale used to open the registry by its path, so a
// `.handover` that is a link to another folder was followed and the registry
// there was read; a broken link read as "no registry yet". Add refused both. The
// premise is asserted: the registry the link points at is one Stale would list.
func TestStale_RefusesADirectoryThatIsALinkOrNotADirectory(t *testing.T) {
	elsewhere, clock := newTestStore(t)
	addAt(t, elsewhere, clock, -30*day, "leaked-if-followed", "open")
	clock.now = t0
	res, err := elsewhere.Stale(7)
	require.NoError(t, err)
	require.Equal(t, []string{"leaked-if-followed"}, staleKeys(res), "premise: that registry has a finding Stale would list")
	listing := dirNames(t, elsewhere.Dir)

	newStore := func(t *testing.T) *Store {
		s, _ := newTestStore(t)
		s.Now = clock.Now
		return s
	}

	t.Run("valid link", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, os.Symlink(elsewhere.Dir, s.Dir))
		res, err := s.Stale(7)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symbolic link")
		assert.Empty(t, res.Rows, "nothing of the registry behind the link")
		assert.Empty(t, res.Notes)
		assert.Equal(t, listing, dirNames(t, elsewhere.Dir), "and nothing was written there")
	})
	t.Run("broken link is a link, not an absent registry", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), s.Dir))
		res, err := s.Stale(7)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symbolic link")
		assert.Empty(t, res.Notes, "it must not say that there is no registry yet")
	})
	t.Run("a regular file", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, os.WriteFile(s.Dir, []byte("x"), 0o600))
		_, err := s.Stale(7)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})
	t.Run("a directory that really is absent is still no registry, and stays absent", func(t *testing.T) {
		s := newStore(t)
		res, err := s.Stale(7)
		require.NoError(t, err)
		assert.Equal(t, []string{"no registry yet at " + s.RegistryPath()}, res.Notes)
		assert.NoDirExists(t, s.Dir)
	})
}

// Both notes at once: the registry mode first, then the missing .gitignore.
func TestAdd_NotesComeInOrderModeThenGitignore(t *testing.T) {
	s, _ := newTestStore(t)
	handMadeDir(t, s)
	mustAdd(t, s, inputFor("k"))
	require.NoError(t, os.Chmod(s.RegistryPath(), 0o644))

	res := mustAdd(t, s, inputFor("k2"))
	assert.Equal(t, []string{"the registry had mode 0644, it is 0600 now", noGitignoreNote}, res.Notes)
}
