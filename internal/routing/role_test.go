package routing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSendPair(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		from      string
		to        string
		allowMesh bool
		wantErr   bool
		errIs     error // optional sentinel
	}{
		// Canonical val<->esc (must always work)
		{"val -> esc", "val", "esc", false, false, nil},
		{"esc -> val", "esc", "val", false, false, nil},

		// Multi-val
		{"val -> val", "val", "val", false, false, nil},

		// Triadic
		{"val -> architect", "val", "architect", false, false, nil},
		{"architect -> val", "architect", "val", false, false, nil},

		// esc<->esc: forbidden default, allowed with mesh
		{"esc -> esc default forbidden", "esc", "esc", false, true, ErrEscToEscForbidden},
		{"esc -> esc with mesh allowed", "esc", "esc", true, false, nil},

		// Observer cannot send (mesh flag does NOT relax this)
		{"observer -> val forbidden", "observer", "val", false, true, ErrObserverCannotSend},
		{"observer -> esc forbidden even with mesh", "observer", "esc", true, true, ErrObserverCannotSend},

		// val -> observer is allowed (observer is a sink for events)
		{"val -> observer", "val", "observer", false, false, nil},

		// neutral compat (v1 read default)
		{"neutral -> val", "neutral", "val", false, false, nil},
		{"val -> neutral", "val", "neutral", false, false, nil},
		{"neutral -> neutral", "neutral", "neutral", false, false, nil},
		{"esc -> neutral", "esc", "neutral", false, false, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSendPair(tc.from, tc.to, tc.allowMesh)
			if tc.wantErr {
				assert.Error(t, err)
				if tc.errIs != nil {
					assert.ErrorIs(t, err, tc.errIs)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateSendPair_ErrorMessageIncludesOverrideHint(t *testing.T) {
	t.Parallel()

	err := ValidateSendPair("esc", "esc", false)
	assert.ErrorContains(t, err, "esc")
	assert.ErrorContains(t, err, "--allow-mesh",
		"BUG-3 fix UX: error must include the override hint for caller discoverability")
}

// A critic sends to its val and to nobody else. Structural, like observer: the
// point is not that it is refused, but that NO FLAG opens it — the rule protects
// the critic's independence, and an operator cannot consent that away on behalf
// of the thing that makes a second opinion worth having.
func TestValidateSendPair_CriticSendsOnlyToVal(t *testing.T) {
	t.Parallel()
	assert.NoError(t, ValidateSendPair("critic", "val", false), "the one road a critic has")

	for _, to := range []string{"esc", "critic", "architect", "observer", "neutral"} {
		err := ValidateSendPair("critic", to, false)
		require.Error(t, err, "critic → %s must be refused", to)
		assert.ErrorIs(t, err, ErrCriticMustGoThroughVal)
		assert.Contains(t, err.Error(), "tell the val instead",
			"the error must show the road that exists, not only the one that does not")

		// The branch nobody names: --allow-mesh relaxes esc↔esc and must not
		// touch this one. A flag that quietly widened would undo the rule for
		// exactly the operator who did not know it was there.
		assert.ErrorIs(t, ValidateSendPair("critic", to, true), ErrCriticMustGoThroughVal,
			"--allow-mesh must not open critic → %s", to)
	}
}

// The other direction stays open: a val briefs its critic. The rule is on who
// SENDS, and reading it as a symmetric ban would mute the role entirely.
func TestValidateSendPair_ValStillReachesItsCritic(t *testing.T) {
	t.Parallel()
	assert.NoError(t, ValidateSendPair("val", "critic", false))
	assert.NoError(t, ValidateSendPair("val", "architect", false), "architect is untouched: it is Claude Desktop's")
}
