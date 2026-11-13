//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-173: `notify-watch` refuses on Windows, and the REASON it gives is what an agent
// acts on. It used to say that a Codex CLI on Windows "has no external wake yet", which
// stopped being true when `codex queue` appeared: an agent that read it concluded that
// a Codex there cannot be woken and did not look for the way that exists.
//
// The refusal stays (there is no watcher here), and so does the fact that the flags are
// not looked at: parsing them to then refuse would suggest some combination might work.
func TestNotifyWatchWindows_RefusesAndPointsToCodexQueue(t *testing.T) {
	err := runNotifyWatch([]string{"--session-id=nwwin001", "--", "echo", "wake"})
	require.ErrorIs(t, err, errNotifyWatchUnsupported, "it still refuses, whatever the flags")

	msg := err.Error()
	assert.Contains(t, msg, "codex queue --thread", "the way to wake a Codex from outside, as it is typed")
	assert.Contains(t, msg, "CODEX_THREAD_ID", "and where the peer reads the id it needs")
	assert.NotContains(t, strings.ToLower(msg), "no external wake", "the premise that expired is not stated again")
	assert.Contains(t, msg, "yourself", "it says that nothing runs the command here: the reader has to")
}
