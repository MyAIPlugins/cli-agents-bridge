package session

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGrantVal registers a val session the way `join` does and returns it.
func newGrantVal(t *testing.T) (*Manager, string, RegisterOpts) {
	t.Helper()
	mgr := NewManager(t.TempDir(), time.Second)
	proj := t.TempDir()
	opts := RegisterOpts{ProjectPath: proj, AgentName: "VAL-grant", Role: RoleVal, Scope: proj}
	mf, release, err := mgr.Register(context.Background(), opts)
	require.NoError(t, err)
	require.NoError(t, release())
	return mgr, mf.SessionID, opts
}

func TestDesktopGrant_OpenReadRoundtripAndValidity(t *testing.T) {
	t.Parallel()
	mgr, sid, _ := newGrantVal(t)
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	mgr.Now = func() time.Time { return t0 }

	_, found, err := mgr.ReadDesktopGrant(sid)
	require.NoError(t, err)
	require.False(t, found, "a val starts with the bridge closed")

	g, err := mgr.OpenDesktopGrant(sid, 8*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, t0, g.OpenedAt)
	assert.Equal(t, t0.Add(8*time.Hour), g.ExpiresAt)

	got, found, err := mgr.ReadDesktopGrant(sid)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, g, *got)

	info, err := os.Stat(mgr.desktopGrantPath(sid))
	require.NoError(t, err)
	assertPOSIXPerm(t, info.Mode().Perm(), 0o600, "desktop-grant.json")

	assert.True(t, got.ValidAt(t0))
	assert.True(t, got.ValidAt(t0.Add(8*time.Hour-time.Nanosecond)))
	assert.False(t, got.ValidAt(t0.Add(8*time.Hour)), "the expiry instant itself is already closed")
	assert.False(t, got.ValidAt(t0.Add(9*time.Hour)))
}

func TestDesktopGrant_OpenRefusesANonPositiveTTL(t *testing.T) {
	t.Parallel()
	mgr, sid, _ := newGrantVal(t)
	for _, ttl := range []time.Duration{0, -time.Minute} {
		_, err := mgr.OpenDesktopGrant(sid, ttl)
		require.Error(t, err, "ttl %s", ttl)
	}
	_, found, err := mgr.ReadDesktopGrant(sid)
	require.NoError(t, err)
	assert.False(t, found, "a refused open writes nothing")
}

func TestDesktopGrant_CloseIsIdempotent(t *testing.T) {
	t.Parallel()
	mgr, sid, _ := newGrantVal(t)
	require.NoError(t, mgr.CloseDesktopGrant(sid), "closing what is not open is not an error")
	_, err := mgr.OpenDesktopGrant(sid, time.Hour)
	require.NoError(t, err)
	require.NoError(t, mgr.CloseDesktopGrant(sid))
	_, found, err := mgr.ReadDesktopGrant(sid)
	require.NoError(t, err)
	assert.False(t, found)
	require.NoError(t, mgr.CloseDesktopGrant(sid))
}

// A damaged file is an error the caller must see, and an empty one is not open:
// neither may read as "forever".
func TestDesktopGrant_UnreadableOrExpiryLessIsNeverOpen(t *testing.T) {
	t.Parallel()
	mgr, sid, _ := newGrantVal(t)
	path := mgr.desktopGrantPath(sid)

	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	_, found, err := mgr.ReadDesktopGrant(sid)
	require.Error(t, err)
	assert.False(t, found)

	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
	g, found, err := mgr.ReadDesktopGrant(sid)
	require.NoError(t, err)
	require.True(t, found)
	assert.False(t, g.ValidAt(time.Now()), "a grant with no expiry is not open")
}

// D8 by construction. The grant is a file of its own, so no writer of the
// manifest can close it, renew it or resurrect it: a re-join, a resume, a
// heartbeat, a state change, a rename. The bytes are compared, not the meaning.
func TestDesktopGrant_NoManifestWriterTouchesIt(t *testing.T) {
	t.Parallel()
	mgr, sid, opts := newGrantVal(t)
	_, err := mgr.OpenDesktopGrant(sid, 8*time.Hour)
	require.NoError(t, err)
	before, err := os.ReadFile(mgr.desktopGrantPath(sid))
	require.NoError(t, err)

	require.NoError(t, mgr.SetState(sid, StateOrchestrating))
	require.NoError(t, mgr.touchHeartbeat(sid))
	require.NoError(t, mgr.AdoptPID(sid))
	require.NoError(t, mgr.RenameAgent(sid, "VAL-renamed"))
	require.NoError(t, mgr.SetRole(sid, RoleVal))

	// The re-join itself: the resume path every `join` and `register --resume`
	// goes through (tryReuse -> adoptAndBackfill).
	opts.AgentName = "VAL-renamed"
	opts.Resume = true
	mf, release, err := mgr.Register(context.Background(), opts)
	require.NoError(t, err)
	require.NoError(t, release())
	require.Equal(t, sid, mf.SessionID, "the re-join resumed the same session")

	after, err := os.ReadFile(mgr.desktopGrantPath(sid))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the re-join neither closed the grant nor renewed it")
}

// Only an explicit open extends the grant.
func TestDesktopGrant_OnlyAnExplicitOpenRenews(t *testing.T) {
	t.Parallel()
	mgr, sid, opts := newGrantVal(t)
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	mgr.Now = func() time.Time { return t0 }
	first, err := mgr.OpenDesktopGrant(sid, 8*time.Hour)
	require.NoError(t, err)

	mgr.Now = func() time.Time { return t0.Add(3 * time.Hour) }
	opts.Resume = true
	_, release, err := mgr.Register(context.Background(), opts)
	require.NoError(t, err)
	require.NoError(t, release())
	got, _, err := mgr.ReadDesktopGrant(sid)
	require.NoError(t, err)
	assert.Equal(t, first.ExpiresAt, got.ExpiresAt, "a re-join three hours later does not move the expiry")

	renewed, err := mgr.OpenDesktopGrant(sid, 8*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, t0.Add(11*time.Hour), renewed.ExpiresAt, "a second open is the renewal")
}

// open and close racing from different processes can only ever leave a whole
// file behind: a reader never sees half of either.
func TestDesktopGrant_ConcurrentOpenCloseNeverTearsTheFile(t *testing.T) {
	mgr, sid, _ := newGrantVal(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	writer := func(open bool) {
		defer wg.Done()
		for i := 0; i < 150; i++ {
			if open {
				if _, err := mgr.OpenDesktopGrant(sid, time.Hour); err != nil {
					t.Errorf("open: %v", err)
					return
				}
			} else if err := mgr.CloseDesktopGrant(sid); err != nil {
				t.Errorf("close: %v", err)
				return
			}
		}
	}
	wg.Add(2)
	go writer(true)
	go writer(false)

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, _, err := mgr.ReadDesktopGrant(sid); err != nil {
				t.Errorf("torn read: %v", err)
				return
			}
		}
	}()

	wg.Wait()
	close(stop)
	<-readerDone
}
