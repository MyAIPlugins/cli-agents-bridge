package routing

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
