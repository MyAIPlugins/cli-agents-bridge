package session

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommitWakeCursor_PrunesBothHalves: the invariant is declared on the
// CURSOR, so honouring it on `notified` alone left `replayed` holding ghost ids
// forever — a message can vanish from the mailbox between the join that armed
// the replay and the next that would have consumed the marker.
func TestCommitWakeCursor_PrunesBothHalves(t *testing.T) {
	t.Parallel()
	const sid = "prune001"
	mgr := NewManager(t.TempDir(), time.Second)
	require.NoError(t, os.MkdirAll(mgr.sessionDir(sid), 0o700))
	plantManifestDetails(t, mgr, sid, "/repo/x", "/repo/x", "ESC-x", RoleEsc)

	now := time.Now().UTC()
	_, err := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}, now, nil, nil)
	require.NoError(t, err)
	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-aaaaaaaaaaaa"}))

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	require.True(t, cursor.WasReplayed("msg-aaaaaaaaaaaa"), "armed for replay")

	// The message disappears from the mailbox before any next re-emits it.
	present := map[string]bool{"msg-bbbbbbbbbbbb": true}
	_, err = mgr.CommitWakeCursor(sid, nil, now, nil, present)
	require.NoError(t, err)

	cursor, _, err = mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.False(t, cursor.WasReplayed("msg-aaaaaaaaaaaa"), "a ghost id must not survive in replayed either")
	assert.True(t, cursor.IsNotified("msg-bbbbbbbbbbbb"), "what is still present stays")
}
