package routing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-72 D7': an architect (Claude Desktop) and a val, and nobody else, in BOTH
// directions. The first test is the outbound half.
func TestValidateSendPair_ArchitectSendsOnlyToVal(t *testing.T) {
	t.Parallel()
	assert.NoError(t, ValidateSendPair("architect", "val", false), "the one road an architect has")

	for _, to := range []string{"esc", "critic", "architect", "observer", "neutral"} {
		err := ValidateSendPair("architect", to, false)
		require.Error(t, err, "architect → %s must be refused", to)
		assert.ErrorIs(t, err, ErrArchitectMustGoThroughVal)
		assert.Contains(t, err.Error(), "tell the val instead",
			"the error must show the road that exists, not only the one that does not")

		// allowMesh relaxes esc↔esc and nothing else.
		assert.ErrorIs(t, ValidateSendPair("architect", to, true), ErrArchitectMustGoThroughVal,
			"allowMesh must not open architect → %s", to)
	}
}

// The inbound half, which is the one that was open: an executor could write to
// the architect directly.
func TestValidateSendPair_OnlyValAddressesArchitect(t *testing.T) {
	t.Parallel()
	for _, from := range []string{"esc", "neutral", "unknown-future-role"} {
		err := ValidateSendPair(from, "architect", false)
		require.Error(t, err, "%s → architect must be refused", from)
		assert.ErrorIs(t, err, ErrOnlyValMayAddressArchitect)
		assert.ErrorIs(t, ValidateSendPair(from, "architect", true), ErrOnlyValMayAddressArchitect,
			"allowMesh must not open %s → architect", from)
	}
}

// The wall is added AFTER the older ones, so each older rule keeps answering
// with its own sentinel: a critic or an observer addressing an architect is told
// about ITS road, not the architect's.
func TestValidateSendPair_OlderRulesKeepTheirOwnError(t *testing.T) {
	t.Parallel()
	assert.ErrorIs(t, ValidateSendPair("critic", "architect", false), ErrCriticMustGoThroughVal)
	assert.ErrorIs(t, ValidateSendPair("observer", "architect", false), ErrObserverCannotSend)
}

// val ↔ architect is the pair the whole port exists for.
func TestValidateSendPair_ValArchitectStillOpenBothWays(t *testing.T) {
	t.Parallel()
	assert.NoError(t, ValidateSendPair("val", "architect", false))
	assert.NoError(t, ValidateSendPair("architect", "val", false))
}
