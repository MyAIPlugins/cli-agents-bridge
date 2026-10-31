package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

var badSessionIDs = []string{"", "../x", "..", "a/b", "UPPER123", "ab", "has space", "../../etc"}

// SC-4: a session id is checked before it builds a path, by the methods that take
// one. The MCP server reads the grant of a val whose id comes out of a manifest, so
// the guard cannot live only in the CLI's resolver.
func TestDesktopGrant_AnInvalidSessionIDNeverBuildsAPath(t *testing.T) {
	t.Parallel()
	mgr, _, _ := newGrantVal(t)

	// A grant file where "../x" would land: the sessions/ directory's sibling.
	outside := filepath.Join(mgr.DataDir, "x", desktopGrantFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(outside), 0o700))
	require.NoError(t, os.WriteFile(outside, []byte(`{"schemaVersion":1,"openedAt":"2026-01-01T00:00:00Z","expiresAt":"2099-01-01T00:00:00Z"}`), 0o600))

	for _, id := range badSessionIDs {
		g, found, err := mgr.ReadDesktopGrant(id)
		require.Error(t, err, "read %q", id)
		assert.ErrorIs(t, err, security.ErrInvalidSessionID, "read %q", id)
		assert.Nil(t, g)
		assert.False(t, found)

		_, err = mgr.OpenDesktopGrant(id, time.Hour)
		assert.ErrorIs(t, err, security.ErrInvalidSessionID, "open %q", id)

		assert.ErrorIs(t, mgr.CloseDesktopGrant(id), security.ErrInvalidSessionID, "close %q", id)
	}
	assert.FileExists(t, outside, "a close with a path as the id removed nothing")
	before, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Contains(t, string(before), "2099", "and an open with one wrote nothing")
}

// A grant in a format this binary does not know is never read as open: a newer
// binary may have changed what the fields MEAN, and an authorization file is the
// one place where guessing is not acceptable.
func TestDesktopGrant_OnlyTheFormatThisBinaryKnowsCanBeOpen(t *testing.T) {
	t.Parallel()
	mgr, sid, _ := newGrantVal(t)
	path := mgr.desktopGrantPath(sid)
	future := `"openedAt":"2026-01-01T00:00:00Z","expiresAt":"2099-01-01T00:00:00Z"`

	// A newer version is an ERROR with the reason, so the val is told to update
	// instead of seeing a bridge that is closed for no visible cause.
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":2,`+future+`}`), 0o600))
	g, found, err := mgr.ReadDesktopGrant(sid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version 2")
	assert.Nil(t, g)
	assert.False(t, found)

	// A file with no version, or an older number, reads as present but is never open,
	// whatever its expiry says.
	for _, body := range []string{`{` + future + `}`, `{"schemaVersion":0,` + future + `}`, `{"schemaVersion":-1,` + future + `}`} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		g, found, err = mgr.ReadDesktopGrant(sid)
		require.NoError(t, err, body)
		require.True(t, found, body)
		assert.False(t, g.ValidAt(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)), "a grant without the known version is not open: %s", body)
	}

	// The known version with the same expiry IS open: the check is on the version,
	// not on the date.
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":1,`+future+`}`), 0o600))
	g, _, err = mgr.ReadDesktopGrant(sid)
	require.NoError(t, err)
	assert.True(t, g.ValidAt(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
}

// A field this binary does not know makes the file unreadable, not tolerated: the
// project reads messages leniently so a newer peer's additions do not break an
// older one, and that is the opposite of what an authorization wants.
func TestDesktopGrant_AFieldItDoesNotKnowMakesItUnreadable(t *testing.T) {
	t.Parallel()
	mgr, sid, _ := newGrantVal(t)
	body := `{"schemaVersion":1,"openedAt":"2026-01-01T00:00:00Z","expiresAt":"2099-01-01T00:00:00Z","scopes":["*"]}`
	require.NoError(t, os.WriteFile(mgr.desktopGrantPath(sid), []byte(body), 0o600))
	g, found, err := mgr.ReadDesktopGrant(sid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scopes")
	assert.Nil(t, g)
	assert.False(t, found)

	// And a second JSON value after the first does not slip in behind it.
	require.NoError(t, os.WriteFile(mgr.desktopGrantPath(sid), []byte(`{"schemaVersion":1,"openedAt":"2026-01-01T00:00:00Z","expiresAt":"2099-01-01T00:00:00Z"}{"x":1}`), 0o600))
	_, _, err = mgr.ReadDesktopGrant(sid)
	require.Error(t, err)
}

// The clock a grant is judged by is the manager's, the one tests inject.
func TestDesktopGrant_NowUTCIsTheManagersClock(t *testing.T) {
	t.Parallel()
	mgr, _, _ := newGrantVal(t)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	mgr.Now = func() time.Time { return at }
	assert.Equal(t, at.UTC(), mgr.NowUTC())
	assert.Equal(t, time.UTC, mgr.NowUTC().Location())
}
