package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// F-172 on the Claude Desktop surface (the MCP server).

func TestScanInboxWithin_ADeniedFileIsUnreadableAndNotCorrupt(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeQuery, "brief", inboxT0)
	plantInboxAt(t, dataDir, inboxSID, "msg-bbbbbbbbbbbb", "valinbox1", message.TypeQuery, "denied", inboxT0)
	inbox := filepath.Join(sessionDirOf(dataDir), "inbox")
	denyReading(t, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "msg-broken000000.json"), []byte("{nope"), 0o600))

	scan, err := scanInboxWithin(context.Background(), &scanGuard{}, inbox, 65536)
	require.NoError(t, err)

	assert.Len(t, scan.entries, 1)
	assert.Equal(t, []string{"msg-broken000000.json"}, scan.corrupt, "only what was read and does not decode")
	assert.Equal(t, []unreadableFile{{File: "msg-bbbbbbbbbbbb.json", Cause: "permission denied"}}, scan.unreadable)
}

func TestRenderNext_UnreadableFilesAreNamedWithTheirCauseAndSaidNotToArrive(t *testing.T) {
	t.Parallel()
	v := view(nil, nil, 0, 24576)
	v.unreadable = []unreadableFile{{File: "msg-bbbbbbbbbbbb.json", Cause: "permission denied"}}

	out := joined(renderNext(mcp.NewPlanner(), v))

	assert.Contains(t, out, `1 file(s) in the inbox cannot be read and are NOT shown or delivered ("msg-bbbbbbbbbbbb.json" (permission denied))`)
	assert.Contains(t, out, "A person has to fix the cause on the machine or move the file(s) out of the inbox")
	assert.Contains(t, out, "until then those messages will not arrive")
	assert.NotContains(t, out, "left alone", "the old text, which said nothing was wrong")
	assert.NotContains(t, out, string(filepath.Separator)+"inbox", "ids, causes and counts only: no path")
}

func TestRenderNext_TheNoticeNamesAtMostFiveUnreadableFiles(t *testing.T) {
	t.Parallel()
	var files []unreadableFile
	for i := 1; i <= 8; i++ {
		files = append(files, unreadableFile{File: fmt.Sprintf("msg-%012d.json", i), Cause: "input/output error"})
	}
	v := view(nil, nil, 0, 24576)
	v.unreadable = files

	out := joined(renderNext(mcp.NewPlanner(), v))

	assert.Contains(t, out, "8 file(s)")
	assert.Contains(t, out, `"msg-000000000005.json" (input/output error)`)
	assert.NotContains(t, out, "msg-000000000006.json", "counted, not named")
	assert.Contains(t, out, "and 3 more")
}

func TestRenderNext_ACorruptFileIsSaidNotToArriveEither(t *testing.T) {
	t.Parallel()
	out := joined(renderNext(mcp.NewPlanner(), view(nil, nil, 2, 24576)))

	assert.Contains(t, out, "2 file(s) in the inbox are not valid messages and are NOT shown or delivered")
	assert.Contains(t, out, "the sender has to send the message again")
	assert.NotContains(t, out, "left alone")
}

// longUnreadable makes count unreadable files whose names are prefixLen "x" plus a
// two-digit number and ".json": the filesystem lets a name be 255 bytes, and the scan
// accepts any name that ends in .json, not only the ids the bridge makes (CRI, F-172).
func longUnreadable(prefixLen, count int) []unreadableFile {
	files := make([]unreadableFile, count)
	for i := range files {
		files[i] = unreadableFile{File: strings.Repeat("x", prefixLen) + fmt.Sprintf("%02d.json", i), Cause: "permission denied"}
	}
	return files
}

// The notice must never be what keeps a readable message from being presented: the
// limit of five names is a count and a name is up to 255 bytes, so five names can take
// the whole budget. The result of one call is checked from the floor the project's own
// tests use, with names from short to the longest the filesystem allows and from one
// file to fifty, and it has to hold BOTH: the readable message is in the result, and so
// is the notice with its count, its cause and what to do. A notice dropped to make room
// would pass the first half and hide a message that will not arrive. The forms of the
// notice change at a byte of budget and a flip is where a count by one would show, so the
// two scenes with the longest names are swept at every byte up to where the fullest form
// fits (the sweep costs seconds under -race, so the others are sampled).
func TestRenderNext_AReadableMessageIsPresentedWhateverTheUnreadableNamesWeigh(t *testing.T) {
	t.Parallel()
	msg := []visibleMessage{vis("msg-aaaaaaaaaaaa", "query", "READABLE-CONTROL", 1)}
	check := func(prefix, count, budget int) {
		v := view(msg, nil, 0, budget)
		v.unreadable = longUnreadable(prefix, count)
		res := renderNext(mcp.NewPlanner(), v)
		out := joined(res)
		where := fmt.Sprintf("names of %d bytes, %d files, budget %d", prefix+7, count, budget)

		require.Contains(t, out, "msg-aaaaaaaaaaaa", "the readable message is presented: %s", where)
		require.Contains(t, out, fmt.Sprintf("%d file(s) in the inbox cannot be read and are NOT shown or delivered", count), "the notice keeps the count: %s", where)
		require.Contains(t, out, "permission denied", "the notice keeps the cause: %s", where)
		require.Contains(t, out, "until then those messages will not arrive", "the notice keeps what to do: %s", where)
		line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1234567890, "result": res})
		require.NoError(t, err)
		require.LessOrEqual(t, len(line), budget, "the result fits: %s", where)
	}
	for _, scene := range [][2]int{{248, 5}, {230, 50}} {
		for budget := 2048; budget <= 4200; budget++ {
			check(scene[0], scene[1], budget)
		}
	}
	for _, prefix := range []int{5, 100, 230, 248} {
		for _, count := range []int{1, 5, 50} {
			for budget := 2048; budget <= 9000; budget += 53 {
				check(prefix, count, budget)
			}
		}
	}
}

// Fewer names first, then the count alone: as the budget narrows the notice names
// five, then four, down to none, and never more names at a smaller budget.
func TestRenderNext_TheUnreadableNoticeNamesFewerFilesAsTheBudgetNarrowsDownToTheCount(t *testing.T) {
	t.Parallel()
	msg := []visibleMessage{vis("msg-aaaaaaaaaaaa", "query", "READABLE-CONTROL", 1)}
	nameStart := `"` + strings.Repeat("x", 230)
	seen := map[int]bool{}
	previous := -1
	for budget := 1000; budget <= 9000; budget++ {
		v := view(msg, nil, 0, budget)
		v.unreadable = longUnreadable(230, 5)
		named := strings.Count(joined(renderNext(mcp.NewPlanner(), v)), nameStart)
		require.GreaterOrEqual(t, named, previous, "budget %d names %d files after %d at a smaller one", budget, named, previous)
		previous = named
		seen[named] = true
	}
	for want := 0; want <= maxUnreadableListed; want++ {
		assert.True(t, seen[want], "no budget showed exactly %d names: %v", want, seen)
	}
}

// With no name left the notice is still a notice: the count, the causes (distinct, not
// one per file) and the instruction. The names are said not to be listed, so a reader
// does not take the missing names for a missing problem.
func TestRenderNext_WhenNoNameFitsTheNoticeKeepsTheCountTheCausesAndWhatToDo(t *testing.T) {
	t.Parallel()
	msg := []visibleMessage{vis("msg-aaaaaaaaaaaa", "query", "READABLE-CONTROL", 1)}
	files := longUnreadable(248, 50)
	files[7].Cause = "input/output error"
	v := view(msg, nil, 0, 1500)
	v.unreadable = files

	out := joined(renderNext(mcp.NewPlanner(), v))

	assert.NotContains(t, out, "xxxxxxxx", "no name fits at this budget")
	assert.Contains(t, out, "50 file(s) in the inbox cannot be read and are NOT shown or delivered")
	assert.Contains(t, out, "names not listed")
	assert.Contains(t, out, "permission denied")
	assert.Contains(t, out, "input/output error")
	assert.Equal(t, 1, strings.Count(out, "permission denied"), "a cause once, not once per file")
	assert.Contains(t, out, "A person has to fix the cause on the machine or move the file(s) out of the inbox")
	assert.Contains(t, out, "msg-aaaaaaaaaaaa", "and the readable message is presented")
}

// The notice of the corrupt files is a count and a sentence of fixed size, and the
// held-back notice is bounded by its ten ids: next to the worst unreadable notice they
// leave room for a message at the floor the project's tests use (CRI asked to check it).
func TestRenderNext_TheCorruptAndHeldNoticesLeaveRoomForAMessageNextToTheUnreadableOne(t *testing.T) {
	t.Parallel()
	msg := []visibleMessage{vis("msg-aaaaaaaaaaaa", "query", "READABLE-CONTROL", 1)}
	var held []withheld
	for i := 0; i < 12; i++ {
		held = append(held, withheld{ID: fmt.Sprintf("msg-%012d", i), Reason: []withheldReason{reasonNotVal, reasonOtherScope, reasonNotOpen, reasonUnresolvable}[i%4]})
	}
	for _, budget := range []int{2048, 2560, 4096} {
		onlyCorrupt := joined(renderNext(mcp.NewPlanner(), view(msg, held, 99999, budget)))
		assert.Contains(t, onlyCorrupt, "msg-aaaaaaaaaaaa", "held and corrupt: budget %d", budget)

		v := view(msg, held, 99999, budget)
		v.unreadable = longUnreadable(248, 5)
		all := joined(renderNext(mcp.NewPlanner(), v))
		assert.Contains(t, all, "msg-aaaaaaaaaaaa", "held, corrupt and five long unreadable: budget %d", budget)
		assert.Contains(t, all, "5 file(s) in the inbox cannot be read", "budget %d", budget)
	}
}

// The worst scene the notices can make together: the bridge closed, twelve messages held
// back, corrupt files, and fifty unreadable files of the longest names with five different
// causes of 200 bytes each. Below some budget the fixed parts and the notices take the
// whole result whatever form the unreadable notice has, so the sweep starts at 2560, above
// that point; from there the readable message is presented in every byte of budget: the
// notice takes its compact form and what is left is counted from the text that is written,
// separators included. If the texts ever grow past that floor, this test says so.
func TestRenderNext_AReadableMessageIsPresentedEvenWithEveryNoticeAtItsWorst(t *testing.T) {
	t.Parallel()
	msg := []visibleMessage{vis("msg-aaaaaaaaaaaa", "query", "READABLE-CONTROL", 1)}
	var held []withheld
	for i := 0; i < 12; i++ {
		held = append(held, withheld{ID: fmt.Sprintf("msg-%012d", i), Reason: []withheldReason{reasonNotVal, reasonOtherScope, reasonNotOpen, reasonUnresolvable}[i%4]})
	}
	files := longUnreadable(248, 50)
	for i := range files {
		files[i].Cause = fmt.Sprintf("cause %d ", i%5) + strings.Repeat("c", 200)
	}
	for budget := 2560; budget <= 4096; budget++ {
		v := view(msg, held, 99999, budget)
		v.bridgeClosed = true
		v.unreadable = files
		out := joined(renderNext(mcp.NewPlanner(), v))
		require.Contains(t, out, "msg-aaaaaaaaaaaa", "budget %d", budget)
		require.Contains(t, out, "50 file(s) in the inbox cannot be read", "budget %d", budget)
	}
}

// The last form of the notice, the one with no names, has a size of its own: it does not
// grow with the number of files, the length of their names or the length and number of
// the causes, or the notice would be the same defect again one level down. And it is the
// only form that has no name in it.
func TestUnreadableNotices_TheLastFormHasASizeOfItsOwnWhateverTheFilesAre(t *testing.T) {
	t.Parallel()
	small := unreadableNotices(longUnreadable(5, 6))
	files := longUnreadable(248, 5000)
	for i := range files {
		files[i].Cause = fmt.Sprintf("cause %d ", i%50) + strings.Repeat("c", 4000)
	}
	big := unreadableNotices(files)

	require.Len(t, small, maxUnreadableListed+1, "k names for k from five to one, then the form with none")
	require.Len(t, big, maxUnreadableListed+1)
	lastSmall, lastBig := small[len(small)-1], big[len(big)-1]
	assert.Less(t, jsonLen(lastBig), 600, "the form with no names stays small: %d bytes", jsonLen(lastBig))
	assert.Contains(t, lastBig, "5000 file(s)")
	assert.Contains(t, lastBig, "and 47 more", "the causes beyond the third are counted")
	assert.Contains(t, lastBig, "...", "a long cause is cut")
	assert.NotContains(t, lastBig, "xxxx", "no name in the last form")
	assert.NotContains(t, lastSmall, `"`, "no quoted name in the last form")
	assert.Contains(t, lastSmall, "cause: permission denied", "one distinct cause is a cause, in the singular")
	assert.Contains(t, lastBig, "causes: cause 0 ", "several are causes, in the plural")
	for i := 0; i+1 < len(big); i++ {
		assert.Greater(t, jsonLen(big[i]), jsonLen(big[i+1]), "each form is shorter than the one before: form %d", i)
	}
}

// A cause is cut on a rune boundary: a cut in the middle of a character would put an
// invalid sequence in a text a model reads and JSON would have to repair.
func TestCutBytes_CutsOnARuneBoundaryAndSaysItCut(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("\u00e8", 100) // two bytes each

	for _, n := range []int{80, 81, 82} {
		got := cutBytes(long, n)
		assert.True(t, utf8.ValidString(got), "cut at %d bytes: %q", n, got)
		assert.True(t, strings.HasSuffix(got, "..."), "says it cut: %q", got)
		assert.LessOrEqual(t, len(got), n+3, "at most the limit plus the dots")
	}
	assert.Equal(t, "permission denied", cutBytes("permission denied", 80), "a short cause is left alone")
	assert.Equal(t, "abc", cutBytes("abc", 3), "exactly the limit is not a cut")
}

// The notice is counted in the budget by its real text: with unreadable files named
// the result still fits every budget, and the messages are not admitted into the room
// the notice needs.
func TestRenderNext_TheResultFitsEveryBudgetWithUnreadableFilesNamed(t *testing.T) {
	t.Parallel()
	var msgs []visibleMessage
	for i, size := range []int{300, 900, 1500} {
		msgs = append(msgs, vis(fmt.Sprintf("msg-%012d", i), "query", strings.Repeat("a b\"c ", size/6), i+1))
	}
	var files []unreadableFile
	for i := 1; i <= 7; i++ {
		files = append(files, unreadableFile{File: fmt.Sprintf("msg-%012d.json", 100+i), Cause: "permission denied"})
	}
	for budget := 2048; budget <= 9000; budget += 7 {
		v := view(msgs, nil, 2, budget)
		v.unreadable = files
		res := renderNext(mcp.NewPlanner(), v)
		require.Contains(t, joined(res), "cannot be read and are NOT shown", "the premise: the notice is in the result (budget %d)", budget)
		line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1234567890, "result": res})
		require.NoError(t, err)
		require.LessOrEqual(t, len(line), budget, "budget %d", budget)
	}
}
