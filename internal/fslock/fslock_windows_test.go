//go:build windows

package fslock

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// The twin of the Unix rule: only ERROR_LOCK_VIOLATION is contention. This pins the
// mapping; that the call really returns that code is shown by
// TestTryLock_SecondHolderIsBusyAndDeathFreesIt, which cannot pass otherwise.
func TestClassify_OnlyTheBusyCodeIsBusy(t *testing.T) {
	assert.NoError(t, classify("n", nil))
	assert.Equal(t, ErrBusy, classify("n", windows.ERROR_LOCK_VIOLATION))

	for _, code := range []windows.Errno{windows.ERROR_INVALID_HANDLE, windows.ERROR_ACCESS_DENIED, windows.ERROR_NOT_SUPPORTED, windows.ERROR_INVALID_PARAMETER} {
		err := classify("the-file", code)
		require.Error(t, err, code.Error())
		assert.NotErrorIs(t, err, ErrBusy, code.Error())
		assert.True(t, errors.Is(err, code), "the cause stays reachable: %v", code)
		assert.Contains(t, err.Error(), "the-file", "and the file is named")
	}
}
