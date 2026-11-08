package findings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Diff gate round 1 (CRI2 M1). Only the command that creates the directory writes
// its .gitignore; a directory that existed before has none unless its owner put
// one there, and then `git status` shows the registry. The command cannot fix
// that without changing a directory that is not ours, so it says it, every time.

const noGitignoreNote = ".handover/ existed already and has no .gitignore: this command did not add one, make sure git ignores it"

func handMadeDir(t *testing.T, s *Store) {
	t.Helper()
	require.NoError(t, os.Mkdir(s.Dir, 0o700))
}

func TestAdd_SaysWhenAnExistingDirectoryHasNoGitignore(t *testing.T) {
	t.Run("a directory the command created has one: no note", func(t *testing.T) {
		s, _ := newTestStore(t)
		res := mustAdd(t, s, inputFor("k"))
		require.FileExists(t, filepath.Join(s.Dir, gitignoreName), "premise: the creator wrote it")
		assert.Empty(t, res.Notes)
	})
	t.Run("a directory that existed without one: the note, every time", func(t *testing.T) {
		s, _ := newTestStore(t)
		handMadeDir(t, s)
		assert.Equal(t, []string{noGitignoreNote}, mustAdd(t, s, inputFor("k")).Notes)
		assert.Equal(t, []string{noGitignoreNote}, mustAdd(t, s, inputFor("k2")).Notes)
		assert.NoFileExists(t, filepath.Join(s.Dir, gitignoreName), "and the command still does not write it")
	})
	t.Run("once the owner adds one, the note is gone", func(t *testing.T) {
		s, _ := newTestStore(t)
		handMadeDir(t, s)
		require.NoError(t, os.WriteFile(filepath.Join(s.Dir, gitignoreName), []byte("*\n"), 0o600))
		assert.Empty(t, mustAdd(t, s, inputFor("k")).Notes)
	})
	t.Run("a refused row prints no note: nothing was recorded", func(t *testing.T) {
		s, _ := newTestStore(t)
		handMadeDir(t, s)
		bad := inputFor("k")
		bad.Finding = "password=" + "hunter22"
		res, err := s.Add(bad)
		require.Error(t, err)
		assert.Empty(t, res.Notes)
	})
}

func TestStale_SaysWhenAnExistingDirectoryHasNoGitignore(t *testing.T) {
	s, _ := newTestStore(t)
	handMadeDir(t, s)
	mustAdd(t, s, inputFor("k"))

	res, err := s.Stale(7)
	require.NoError(t, err)
	require.NotEmpty(t, res.Notes)
	assert.Equal(t, noGitignoreNote, res.Notes[len(res.Notes)-1])

	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, gitignoreName), []byte("*\n"), 0o600))
	res, err = s.Stale(7)
	require.NoError(t, err)
	assert.NotContains(t, res.Notes, noGitignoreNote)
}

func TestStale_NoGitignoreNoteWhileThereIsNoRegistry(t *testing.T) {
	s, _ := newTestStore(t)
	handMadeDir(t, s)
	res, err := s.Stale(7)
	require.NoError(t, err)
	assert.Equal(t, []string{"no registry yet at " + s.RegistryPath()}, res.Notes, "an empty folder has nothing for git to show")
}
