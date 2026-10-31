//go:build !windows

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// The footer of `next` says how to act on a message (`reply`, `ack`, `read`). With no
// message to act on it is noise: a Cowork called it that on a closed bridge.
const footerLead = "To answer an ask use `reply`"

func TestMCPNext_TheFooterIsOnlyThereWhenThereIsAMessageToActOn(t *testing.T) {
	j := newJoinedDemo(t)

	empty := j.next(0)
	require.False(t, empty.IsError, empty.Text())
	assert.Contains(t, empty.Text(), "0 message(s) open for you")
	assert.NotContains(t, empty.Text(), footerLead, "open, nothing to act on")
	assert.NotContains(t, empty.Text(), "Nothing leaves the inbox")

	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "please review")
	withMessage := j.next(0)
	require.False(t, withMessage.IsError, withMessage.Text())
	assert.Contains(t, withMessage.Text(), footerLead, "a message to act on: the footer is back")

	again := j.next(0)
	assert.Contains(t, again.Text(), footerLead, "and for a pointer line, which is still a message to act on")
}

func TestMCPNext_AClosedBridgeSaysItIsClosedAndNothingAboutHowToAnswer(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "please review")
	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))

	res := j.next(0)
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "No val of this project has the bridge open right now", "the notice stays: it is what the reader needs")
	assert.NotContains(t, res.Text(), footerLead)
	assert.NotContains(t, res.Text(), "please review", "and the closed val's message is not shown")
}
