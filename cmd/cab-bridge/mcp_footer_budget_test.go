//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
)

// The footer of `next` is in the result when there is a message to act on, and the
// budget is counted on the same text: one decision, read by both.

func TestFooterOf_IsTheFooterWhenThereIsAMessageAndNothingWhenThereIsNot(t *testing.T) {
	t.Parallel()
	assert.Empty(t, footerOf(view(nil, nil, 0, 4096)), "nothing to act on")
	assert.Empty(t, footerOf(view(nil, []withheld{{ID: "msg-hhhhhhhhhhhh", Reason: reasonNotOpen}}, 2, 4096)), "held and unreadable messages are not messages to act on")
	assert.Equal(t, nextFooter, footerOf(view([]visibleMessage{vis("msg-aaaaaaaaaaaa", "query", "hello", 1)}, nil, 0, 4096)))
}

// Across every budget the result fits when the footer is in it: the footer's own
// length is part of what the plan subtracts, so a message is never admitted into the
// room the footer needs.
func TestRenderNext_TheResultFitsEveryBudgetWithTheFooterInIt(t *testing.T) {
	t.Parallel()
	var msgs []visibleMessage
	for i, size := range []int{300, 900, 1500, 2600} {
		msgs = append(msgs, vis(fmt.Sprintf("msg-%012d", i), "query", strings.Repeat("a b\"c ", size/6), i+1))
	}
	for budget := 2048; budget <= 9000; budget += 3 {
		res := renderNext(mcp.NewPlanner(), view(msgs, nil, 0, budget))
		require.Contains(t, joined(res), footerLead, "the premise: there are messages, so the footer is there (budget %d)", budget)
		line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1234567890, "result": res})
		require.NoError(t, err)
		require.LessOrEqual(t, len(line), budget, "budget %d", budget)
	}
}

func TestRenderNext_WithNoMessageThereIsNoBlockLeftForTheFooter(t *testing.T) {
	t.Parallel()
	for name, res := range map[string]mcp.Result{
		"nothing":             renderNext(mcp.NewPlanner(), view(nil, nil, 0, 4096)),
		"an unreadable file":  renderNext(mcp.NewPlanner(), view(nil, nil, 1, 4096)),
		"a message held back": renderNext(mcp.NewPlanner(), view(nil, []withheld{{ID: "msg-hhhhhhhhhhhh", Reason: reasonNotOpen}}, 0, 4096)),
		"a closed bridge":     renderNext(mcp.NewPlanner(), nextView{project: "demo", agentName: "ARC-demo-cowork", bridgeClosed: true, now: renderNow, budget: 4096}),
	} {
		for i, block := range texts(res) {
			assert.NotEmpty(t, strings.TrimSpace(block), "%s: block %d is empty", name, i)
		}
	}
}
