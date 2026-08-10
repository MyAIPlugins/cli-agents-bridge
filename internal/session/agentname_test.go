package session

import (
	"context"
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

	got, changed := SanitizeDerivedName("feat@2")
	assert.True(t, changed, "the caller must be able to say it happened")
	assert.Equal(t, "feat-2", got)
	assert.NoError(t, ValidateAgentName(got), "and what comes out must be addressable")

	got, changed = SanitizeDerivedName("esc-v08")
	assert.False(t, changed)
	assert.Equal(t, "esc-v08", got)
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
	assert.Equal(t, "feat-2", mf2.AgentName)
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
	assert.Equal(t, "feat-2", first.AgentName)

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
