package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveMaxBlocking covers the F-26 window precedence: --until-deadline
// flag > CAB_MAX_BLOCKING_SECONDS env (cfgSeconds) > 540s default, plus the
// invalid/non-positive flag errors.
func TestResolveMaxBlocking(t *testing.T) {
	t.Parallel()

	t.Run("flag parses and sets the window", func(t *testing.T) {
		d, err := resolveMaxBlocking("10s", 0)
		require.NoError(t, err)
		assert.Equal(t, 10*time.Second, d)
	})

	t.Run("flag wins over env (precedence flag>env)", func(t *testing.T) {
		d, err := resolveMaxBlocking("2s", 100)
		require.NoError(t, err)
		assert.Equal(t, 2*time.Second, d, "--until-deadline must win over CAB_MAX_BLOCKING_SECONDS")
	})

	t.Run("no flag uses env seconds", func(t *testing.T) {
		d, err := resolveMaxBlocking("", 120)
		require.NoError(t, err)
		assert.Equal(t, 120*time.Second, d)
	})

	t.Run("no flag, no env falls back to 540s", func(t *testing.T) {
		d, err := resolveMaxBlocking("", 0)
		require.NoError(t, err)
		assert.Equal(t, 9*time.Minute, d)
	})

	t.Run("invalid duration is a clear error", func(t *testing.T) {
		_, err := resolveMaxBlocking("2hh", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--until-deadline", "error must name the flag")
		assert.Contains(t, err.Error(), "2hh", "error must echo the bad value")
	})

	t.Run("non-positive duration is rejected", func(t *testing.T) {
		_, err := resolveMaxBlocking("0s", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "positive")

		_, err = resolveMaxBlocking("-5m", 0)
		require.Error(t, err)
	})
}
