package regression

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/myAIPlugins/cli-agents-bridge/internal/routing"
)

// TestBUG3_EscToEscForbiddenByDefault reproduces BUG-3 (Patil multi-peer
// routing without role field allowed an ESC to message another ESC under
// the misconception it was VAL — Alan-reported empirically during
// p1-wp-translator weekend setup).
//
// cli-agents-bridge fix: routing.ValidateSendPair enforces hub-and-spoke
// VAL-centric policy by default. esc↔esc is rejected with
// ErrEscToEscForbidden + "use --allow-mesh" hint. The override is
// explicit, not implicit.
func TestBUG3_EscToEscForbiddenByDefault(t *testing.T) {
	t.Parallel()

	err := routing.ValidateSendPair("esc", "esc", false)
	assert.ErrorIs(t, err, routing.ErrEscToEscForbidden,
		"BUG-3 regression: esc→esc default-forbid contract violated")
	assert.ErrorContains(t, err, "--allow-mesh",
		"BUG-3 fix UX: error must surface override hint")
}

func TestBUG3_EscToEscAllowedWithMesh(t *testing.T) {
	t.Parallel()

	err := routing.ValidateSendPair("esc", "esc", true)
	assert.NoError(t, err, "explicit --allow-mesh override must succeed")
}

func TestBUG3_ValToEscAlwaysWorks(t *testing.T) {
	t.Parallel()

	// Canonical val<->esc workflow must never be blocked
	assert.NoError(t, routing.ValidateSendPair("val", "esc", false))
	assert.NoError(t, routing.ValidateSendPair("esc", "val", false))
}
