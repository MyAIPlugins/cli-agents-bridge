//go:build !windows

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modeOfPath(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}

// BAR 9 with the real binary, whatever umask the caller runs under: the folder
// is created 0700, the files 0600, and an existing folder is left as it is.
func TestFindings_DirAndFileModes(t *testing.T) {
	r := newFindingsRepo(t)
	_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
	require.Equal(t, 0, exit, stderr)

	assert.Equal(t, os.FileMode(0o700), modeOfPath(t, r.handover()))
	assert.Equal(t, os.FileMode(0o600), modeOfPath(t, r.registry()))
	assert.Equal(t, os.FileMode(0o600), modeOfPath(t, filepath.Join(r.handover(), "findings.lock")))
	assert.Equal(t, os.FileMode(0o600), modeOfPath(t, filepath.Join(r.handover(), ".gitignore")))

	existing := newFindingsRepo(t)
	require.NoError(t, os.Mkdir(existing.handover(), 0o755))
	require.NoError(t, os.Chmod(existing.handover(), 0o755))
	_, stderr, exit = existing.run(t, findingJSON(t, nil), "findings", "add")
	require.Equal(t, 0, exit, stderr)
	assert.Equal(t, os.FileMode(0o755), modeOfPath(t, existing.handover()), "an existing folder keeps its mode")
	assert.NoFileExists(t, filepath.Join(existing.handover(), ".gitignore"), "only the creator writes it")
}

// D16: a registry that was 0644 is republished 0600, the old rows are identical
// byte for byte, and the command says so. A refused add changes neither.
func TestFindings_ExistingLooseRegistryIsRepublishedAs0600(t *testing.T) {
	r := newFindingsRepo(t)
	_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
	require.Equal(t, 0, exit, stderr)
	require.NoError(t, os.Chmod(r.registry(), 0o644))
	before, err := os.ReadFile(r.registry())
	require.NoError(t, err)

	_, _, exit = r.run(t, findingJSON(t, map[string]string{"chiave": "second", "azione": "password=" + "hunter22"}), "findings", "add")
	require.Equal(t, 1, exit)
	assert.Equal(t, os.FileMode(0o644), modeOfPath(t, r.registry()), "a refused add leaves the mode")
	again, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	assert.Equal(t, before, again)

	_, stderr, exit = r.run(t, findingJSON(t, map[string]string{"chiave": "second"}), "findings", "add")
	require.Equal(t, 0, exit, stderr)
	assert.Contains(t, stderr, "findings: note: the registry had mode 0644, it is 0600 now\n")
	assert.Equal(t, os.FileMode(0o600), modeOfPath(t, r.registry()))
	after, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after)[:len(before)])
}

func TestFindings_RefusesSymlinkedDirOrFileOrLock(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("untouched"), 0o600))

	t.Run("directory", func(t *testing.T) {
		r := newFindingsRepo(t)
		require.NoError(t, os.Symlink(outside, r.handover()))
		_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
		assert.Equal(t, 1, exit)
		assert.Contains(t, stderr, "symbolic link")
		assert.Equal(t, []string{"victim"}, dirListing(t, outside), "nothing written through the link")
	})
	t.Run("directory, broken link", func(t *testing.T) {
		r := newFindingsRepo(t)
		require.NoError(t, os.Symlink(filepath.Join(outside, "missing"), r.handover()))
		_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
		assert.Equal(t, 1, exit)
		assert.Contains(t, stderr, "symbolic link")
		assert.NoFileExists(t, filepath.Join(outside, "missing"))
	})
	t.Run("registry", func(t *testing.T) {
		r := newFindingsRepo(t)
		require.NoError(t, os.Mkdir(r.handover(), 0o700))
		require.NoError(t, os.Symlink(victim, r.registry()))
		for _, args := range [][]string{{"findings", "add"}, {"findings", "stale"}} {
			_, stderr, exit := r.run(t, findingJSON(t, nil), args...)
			assert.Equal(t, 1, exit, args)
			assert.Contains(t, stderr, "symbolic link", args)
		}
		data, err := os.ReadFile(victim)
		require.NoError(t, err)
		assert.Equal(t, "untouched", string(data))
	})
	t.Run("lock file, valid and broken link", func(t *testing.T) {
		for name, target := range map[string]string{"valid": victim, "broken": filepath.Join(outside, "nowhere")} {
			r := newFindingsRepo(t)
			require.NoError(t, os.Mkdir(r.handover(), 0o700))
			require.NoError(t, os.Symlink(target, filepath.Join(r.handover(), "findings.lock")))
			_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
			assert.Equal(t, 1, exit, name)
			assert.Contains(t, stderr, "symlink", name)
			assert.NoFileExists(t, r.registry(), name)
		}
		assert.NoFileExists(t, filepath.Join(outside, "nowhere"))
		data, err := os.ReadFile(victim)
		require.NoError(t, err)
		assert.Equal(t, "untouched", string(data))
	})
}

// BAR 7 with the real binary: `stale` with a read-only folder still works and
// leaves everything as it found it.
func TestFindings_StaleNeverWritesThroughTheBinary(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions: the premise of this test does not hold")
	}
	r := newFindingsRepo(t)
	_, stderr, exit := r.run(t, findingJSON(t, nil), "findings", "add")
	require.Equal(t, 0, exit, stderr)
	require.NoError(t, os.Remove(filepath.Join(r.handover(), "findings.lock")))
	require.NoError(t, os.Chmod(r.handover(), 0o500))
	t.Cleanup(func() { _ = os.Chmod(r.handover(), 0o700) })
	require.Error(t, os.WriteFile(filepath.Join(r.handover(), "probe"), nil, 0o600), "premise: the folder refuses a new file")

	namesBefore := dirListing(t, r.handover())
	bytesBefore, err := os.ReadFile(r.registry())
	require.NoError(t, err)

	_, stderr, exit = r.run(t, "", "findings", "stale")
	require.Equal(t, 0, exit, stderr)

	assert.Equal(t, namesBefore, dirListing(t, r.handover()), "no file appeared, the lock file included")
	bytesAfter, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	assert.Equal(t, bytesBefore, bytesAfter)
}

const oldTimestamp = "2026-01-01T00:00:00.000Z"

// oldRegistryBytes is a valid registry with one open finding that is long past
// any threshold: a real `add` in a throwaway repository, with the two dates
// rewritten (the binary has no clock to set).
func oldRegistryBytes(t *testing.T, finding string) []byte {
	t.Helper()
	r := newFindingsRepo(t)
	_, stderr, exit := r.run(t, findingJSON(t, map[string]string{"finding": finding}), "findings", "add")
	require.Equal(t, 0, exit, stderr)
	data, err := os.ReadFile(r.registry())
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2)
	fields := strings.Split(lines[1], "\t")
	fields[0], fields[5] = oldTimestamp, oldTimestamp
	return []byte(lines[0] + "\n" + strings.Join(fields, "\t") + "\n")
}

// Diff gate round 1 (CRI B2), the real binary, both commands: a `.handover` that
// is a link to a folder holding a valid, old registry is refused, and none of
// that registry reaches stdout. Premise asserted: the same file in a real
// `.handover` is listed by `stale`.
func TestFindings_BothCommandsRefuseASymlinkedDirectoryAndStaleLeaksNothing(t *testing.T) {
	const finding = "synthetic external finding"
	registry := oldRegistryBytes(t, finding)

	control := newFindingsRepo(t)
	require.NoError(t, os.Mkdir(control.handover(), 0o700))
	require.NoError(t, os.WriteFile(control.registry(), registry, 0o600))
	stdout, stderr, exit := control.run(t, "", "findings", "stale", "--days=1")
	require.Equal(t, 0, exit, stderr)
	require.Contains(t, stdout, finding, "premise: in a real folder this registry is listed")

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "findings.tsv"), registry, 0o600))
	listing := dirListing(t, outside)

	for name, target := range map[string]string{"valid link": outside, "broken link": filepath.Join(outside, "missing")} {
		for _, args := range [][]string{{"findings", "stale", "--days=1"}, {"findings", "add"}} {
			r := newFindingsRepo(t)
			require.NoError(t, os.Symlink(target, r.handover()))
			stdout, stderr, exit := r.run(t, findingJSON(t, nil), args...)

			assert.Equal(t, 1, exit, "%s %v: %s", name, args, stderr)
			assert.Empty(t, stdout, "%s %v: nothing of the registry behind the link", name, args)
			assert.Contains(t, stderr, "symbolic link", "%s %v", name, args)
			assert.NotContains(t, stderr, "no registry yet", "%s %v: a link is not an absent registry", name, args)
			assert.NotContains(t, stdout+stderr, finding)
		}
	}
	assert.Equal(t, listing, dirListing(t, outside), "nothing was written through either link")

	file := newFindingsRepo(t)
	require.NoError(t, os.WriteFile(file.handover(), []byte("x"), 0o600))
	stdout, stderr, exit = file.run(t, "", "findings", "stale")
	assert.Equal(t, 1, exit)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "not a directory")
}
