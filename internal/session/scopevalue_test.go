package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sibling grouping of B-1 is a POLICY — it raises the anti-impersonation
// warning — and it was reading the raw field: for a legacy session the scope was
// empty, ScopeSiblings stayed empty, and the warning disappeared without a word
// in exactly the case it exists for (prefix resolution in a shared repository).
//
// This lives in internal/session and not in cmd because that is where the
// decision lives, and it is why the previous criterion could not reach it.
func TestLookupByCWDDetails_SiblingsUseTheEffectiveScope(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	work, other := filepath.Join(repo, "work"), filepath.Join(repo, "other")
	for _, p := range []string{filepath.Join(repo, ".git"), work, other} {
		require.NoError(t, os.MkdirAll(p, 0o700))
	}
	resolved, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)

	mgr := NewManager(dir, time.Second)
	plant := func(id, projectPath, scope string) {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions", id), 0o700))
		now := time.Now().UTC()
		require.NoError(t, mgr.SaveManifest(&Manifest{
			SessionID: id, SchemaVersion: SchemaVersionV2,
			ProjectName: filepath.Base(projectPath), ProjectPath: projectPath,
			AgentName: "agent-" + id, Role: RoleEsc, PID: os.Getpid(),
			StartedAt: now, LastHeartbeat: now, Status: StatusActive,
			Capabilities: []string{"query"}, Scope: scope,
		}))
	}

	// A LEGACY session in work/ (no scope) and a CURRENT one in other/.
	plant("legacyaa", work, "")
	plant("currentb", other, resolved)

	res, err := mgr.LookupByCWDDetails(work)
	require.NoError(t, err)
	require.Equal(t, "legacyaa", res.SelectedID)
	assert.Len(t, res.ScopeSiblings, 1,
		"a legacy session had no siblings and the B-1 warning vanished silently")
	assert.Equal(t, "currentb", res.ScopeSiblings[0].ID)

	// Candidate carries both: the effective one for the policy, the raw one for
	// the sentence that describes what is on disk.
	assert.Equal(t, resolved, res.Candidates[0].Scope)
	assert.Empty(t, res.Candidates[0].StoredScope, "the record says nothing, and the text must be able to say so")

	// The complement: two DERIVED sessions in different repositories are not
	// siblings, which is the direction that would have merged two projects.
	other2 := filepath.Join(dir, "repo2", "work")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "repo2", ".git"), 0o700))
	require.NoError(t, os.MkdirAll(other2, 0o700))
	plant("legacycc", other2, "")

	res, err = mgr.LookupByCWDDetails(work)
	require.NoError(t, err)
	for _, s := range res.ScopeSiblings {
		assert.NotEqual(t, "legacycc", s.ID, "two derived scopes in different repositories are two projects")
	}
}
