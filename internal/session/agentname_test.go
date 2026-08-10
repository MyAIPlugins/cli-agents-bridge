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
