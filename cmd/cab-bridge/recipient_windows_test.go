//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-135, at the door where it was found: a qualified address carrying an
// ABSOLUTE Windows path must resolve.
//
// It did not, and the shape of the failure is worth keeping: the old test for
// "is this hint absolute" was `strings.HasPrefix(hint, "/")`, so a `C:\...` hint
// went down the BASENAME branch, where a full path can never match — and the
// error then listed the very project it had just refused to find.
//
// The casing is changed only on `MiXeD`, never on the temp dir, so this states a
// property instead of observing how t.TempDir() happened to be spelled.
func TestScopeMatchesHint_AbsoluteWindowsPathResolves(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	project := filepath.Join(base, "MiXeD", "project")
	require.NoError(t, os.MkdirAll(project, 0o700))

	// The stored scope is what the resolver produces, so the test compares the
	// two values the product actually compares.
	scope := session.CanonicalizePath(project)

	assert.True(t, scopeMatchesHint(scope, project),
		"the exact path a reader copies out of `peers --all-scopes` must match")
	assert.True(t, scopeMatchesHint(scope, filepath.Join(base, "MIXED", "project")),
		"the disk's casing and the human's casing name one directory")
	assert.True(t, scopeMatchesHint(scope, "project"),
		"the basename form still works — it is what peers prints when it is unambiguous")

	assert.False(t, scopeMatchesHint(scope, filepath.Join(base, "MiXeD", "other")),
		"a different project must not match")
	assert.False(t, scopeMatchesHint(scope, "other"))
	assert.False(t, scopeMatchesHint("", project),
		"a legacy session with no scope is never matched by an address")
}

// A rooted path with no drive (`/foo` typed literally on Windows) is neither of
// the two things an address can carry: not a full path — it resolves against
// whichever volume the process is on — and not a basename, because it contains a
// separator.
//
// The explanation belongs to the FAILURE, not to the grammar: parseRecipient
// stays pure syntax so that a token the product itself printed always parses
// back. Driven through resolveRecipientByName because that is the function that
// composes this message, and it is the level the sibling case
// (TestResolveRecipient_AmbiguousBasenameFailsClosed) is written at.
func TestResolveRecipient_ARootedPathWithNoDriveSaysWhy(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	planted(t, dataDir, "valmine1", session.RoleVal, "VAL-bridge", filepath.Join(t.TempDir(), "mine"))
	planted(t, dataDir, "valfarrr", session.RoleVal, "VAL-far", filepath.Join(t.TempDir(), "far"))

	_, err := resolveRecipientByName(cfg, mgr, `VAL-far@/foo`, "valmine1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agent named", "the original answer is still there")
	assert.Contains(t, err.Error(), "projects with agents", "and so is the list that answers 'wrong project?'")
	assert.Contains(t, err.Error(), "names no drive", "plus the cause the plain message cannot show")
	assert.Contains(t, err.Error(), "peers --all-scopes", "and where the working value comes from")

	// The same address written properly reaches the same session: the hint is
	// what was wrong, not the name.
	got, rerr := resolveRecipientByName(cfg, mgr, "VAL-far@far", "valmine1")
	require.NoError(t, rerr)
	assert.Equal(t, "valfarrr", got)
}
