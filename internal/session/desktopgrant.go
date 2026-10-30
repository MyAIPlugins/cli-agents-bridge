package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// DesktopGrantSchemaVersion is the on-disk format of desktop-grant.json.
const DesktopGrantSchemaVersion = 1

const desktopGrantFile = "desktop-grant.json"

// DesktopGrant is a val's permission for Claude Desktop to talk to it (F-72, D3).
//
// WHERE IT LIVES is the decision, not an implementation detail: a file of its own
// in the val's session directory, written only by `desktop open` and `desktop
// close`, and NOT a field of manifest.json. Every join, resume, heartbeat and
// state change rewrites the manifest by read-modify-write, and those writers run
// in other processes, so a field in it would be at the mercy of whichever of them
// read the manifest before the grant changed and wrote it back after. A separate
// file is never touched by them, so "a re-join of the val neither closes the grant
// nor renews it" (D8) holds by construction instead of by a lock between
// processes that do not share one.
//
// It is the ONLY authority: the val's role, its scope and this file together say
// whether Desktop may read what that val sent. Nothing is cached across calls.
type DesktopGrant struct {
	SchemaVersion int       `json:"schemaVersion"`
	OpenedAt      time.Time `json:"openedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// ValidAt reports whether the grant is open at now. A grant without an expiry is
// not open: an empty or half-written value must never read as "forever".
func (g DesktopGrant) ValidAt(now time.Time) bool {
	return !g.ExpiresAt.IsZero() && now.Before(g.ExpiresAt)
}

func (m *Manager) desktopGrantPath(sessionID string) string {
	return filepath.Join(m.sessionDir(sessionID), desktopGrantFile)
}

// ReadDesktopGrant loads a val's grant, if it has one. An unreadable file is an
// ERROR, not an absence: the caller treats it as closed and says why. Reading it
// as "no grant" would hide a damaged file; reading it as open would grant on
// garbage.
//
// Does not validate sessionID — callers validate via security.ValidateSessionID.
func (m *Manager) ReadDesktopGrant(sessionID string) (*DesktopGrant, bool, error) {
	data, err := security.ReadOwnedFile(m.desktopGrantPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("desktop grant: read: %w", err)
	}
	var g DesktopGrant
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, false, fmt.Errorf("desktop grant at %s is unreadable (%w) — it counts as closed; `cab-bridge desktop open` writes a new one", m.desktopGrantPath(sessionID), err)
	}
	return &g, true, nil
}

// OpenDesktopGrant opens (or renews) the grant for ttl from now. Renewing is the
// same call: a re-join does NOT call it, so only an explicit open extends it.
// The write is atomic (rename), so a reader sees the old file or the new one,
// never half of either.
func (m *Manager) OpenDesktopGrant(sessionID string, ttl time.Duration) (DesktopGrant, error) {
	if ttl <= 0 {
		return DesktopGrant{}, fmt.Errorf("desktop grant: ttl must be positive, got %s", ttl)
	}
	now := m.now().UTC()
	g := DesktopGrant{SchemaVersion: DesktopGrantSchemaVersion, OpenedAt: now, ExpiresAt: now.Add(ttl)}
	if err := transportfs.AtomicWriteJSON(m.desktopGrantPath(sessionID), g); err != nil {
		return DesktopGrant{}, fmt.Errorf("desktop grant: write: %w", err)
	}
	return g, nil
}

// CloseDesktopGrant removes the grant. Closing a grant that is not open is not an
// error: the end state is the one that was asked for.
func (m *Manager) CloseDesktopGrant(sessionID string) error {
	if err := os.Remove(m.desktopGrantPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("desktop grant: remove: %w", err)
	}
	return nil
}
