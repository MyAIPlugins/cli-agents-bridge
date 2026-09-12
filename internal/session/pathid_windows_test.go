//go:build windows

package session

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The casing is changed ONLY on a segment this test owns (`MiXeD`), never on the
// temp dir: whatever spelling the OS handed back for t.TempDir() is left alone,
// so the result cannot depend on how the temp path happened to be cased. That is
// the difference between a test that states a property and one that observes an
// accident.
func TestPathIdentity_OneDirectoryIsOnePathWhateverTheCasing(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	project := filepath.Join(base, "MiXeD", "project")
	shouted := filepath.Join(base, "MIXED", "project")
	whispered := filepath.Join(base, "mixed", "project")

	assert.True(t, SamePathLexical(project, shouted), "MIXED and MiXeD are one directory")
	assert.True(t, SamePathLexical(project, whispered), "and so are mixed and MiXeD")
	assert.True(t, IsDescendantLexical(filepath.Join(shouted, "internal", "x"), project),
		"a descendant is a descendant whatever the casing on the way down")

	// The other half, which a case-insensitive comparison must NOT swallow:
	// different directories stay different, and the /foo/barbaz trap still holds.
	assert.False(t, SamePathLexical(project, filepath.Join(base, "MiXeD", "other")))
	assert.False(t, IsDescendantLexical(filepath.Join(base, "MiXeDproject"), filepath.Join(base, "MiXeD")),
		"MiXeDproject is not inside MiXeD — the trailing separator is what says so")
}

// "" is not a path and must never be resolved against the current directory: a
// scope that is UNKNOWN has to keep meaning unknown at every call site.
func TestPathIdentity_EmptyIsNotTheCurrentDirectory(t *testing.T) {
	t.Parallel()
	assert.True(t, SamePathLexical("", ""), "unknown and unknown are one group")
	assert.False(t, SamePathLexical("", filepath.Join(t.TempDir(), "x")))
	assert.False(t, IsDescendantLexical(t.TempDir(), ""), "everything is not inside nothing")
}

// The scope axis: two spellings of one project are one project, and a session
// resuming under a differently-cased scope finds its own identity.
func TestScopeIdentity_CasingDoesNotSplitAProject(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	scope := filepath.Join(base, "MiXeD", "project")
	typed := filepath.Join(base, "MIXED", "project")

	assert.True(t, SameProject(scope, typed))
	assert.False(t, CrossesScopes(scope, typed), "a casing difference is not a crossing")

	mf := &Manifest{Scope: scope, ProjectPath: scope}
	assert.True(t, scopeMatches(mf, typed, typed),
		"resume must recognise its own session when the scope is spelled differently")
}

// PathNeedsVolume drives an explicit refusal, so its table says exactly which
// shapes are refused — including the two that must NOT be.
func TestPathNeedsVolume_OnlyRootedWithoutAVolume(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want bool
		why  string
	}{
		{"/foo", true, "rooted, no drive: resolves against whichever volume we are on"},
		{`\foo`, true, "same shape written the Windows way"},
		{`/foo/bar`, true, "depth changes nothing"},
		{"foo/bar", true, "relative WITH a separator: resolves against the cwd, same failure"},
		{`foo\bar`, true, "and the same shape with the Windows separator"},
		{`C:\foo`, false, "a real absolute path"},
		{`c:/foo`, false, "forward slashes and lowercase drive are still a drive"},
		{`\\server\share`, false, "UNC is absolute — refusing it would break a real address"},
		{"repo", false, "a basename: no separator, so nothing is ambiguous"},
		{"", false, "empty is handled by the caller's own check"},
	} {
		assert.Equal(t, tc.want, PathNeedsVolume(tc.in), "%q — %s", tc.in, tc.why)
	}
}
