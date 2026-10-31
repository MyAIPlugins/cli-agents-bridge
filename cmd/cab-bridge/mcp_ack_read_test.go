//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

func (j *joinedDemo) inInbox(id string) bool {
	_, err := os.Stat(filepath.Join(j.inbox, id+".json"))
	return err == nil
}

func (j *joinedDemo) archived(id string) bool {
	matches, _ := filepath.Glob(filepath.Join(j.e.dataDir, "sessions", j.arcSID, "processed", "*-"+id+".json"))
	return len(matches) == 1
}

func (j *joinedDemo) ack(ids ...string) toolResult {
	j.e.t.Helper()
	return j.c.call("ack", map[string]any{"ids": ids})
}

// --- ack ---------------------------------------------------------------------

func TestMCPAck_ArchivesATellAndAResponseAndTheyStopAppearing(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "a tell")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeResponse, "a response")
	require.Contains(t, j.next(0).Text(), "2 message(s) open")

	res := j.ack("msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb")
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "msg-aaaaaaaaaaaa: archived")
	assert.False(t, j.inInbox("msg-aaaaaaaaaaaa"))
	assert.True(t, j.archived("msg-aaaaaaaaaaaa"))
	assert.True(t, j.archived("msg-bbbbbbbbbbbb"))
	assert.Contains(t, j.next(0).Text(), "0 message(s) open")
}

func TestMCPAck_IsIdempotent(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "a tell")
	require.False(t, j.ack("msg-aaaaaaaaaaaa").IsError)
	res := j.ack("msg-aaaaaaaaaaaa")
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "already-archived")
}

// An ask is closed by reply, which is what tells the val it was answered. Acking it
// would make the question disappear with no answer, and nobody would know.
func TestMCPAck_RefusesAnAskAndMovesNothingOfTheBatch(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "a tell")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeQuery, "an ask")

	res := j.ack("msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "closed by `reply`")
	assert.Contains(t, res.Text(), "msg-aaaaaaaaaaaa: not-attempted", "the good one waited: the batch is judged as a whole")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"))
	assert.True(t, j.inInbox("msg-bbbbbbbbbbbb"))
}

func TestMCPAck_AWrongIdInTheMiddleRefusesTheWholeBatch(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "one")
	j.put("msg-cccccccccccc", j.valSID, message.TypeNotify, "three")

	res := j.ack("msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "msg-bbbbbbbbbbbb: refused (no such message)")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"), "nothing moved: a mistake in one position never reads as done")
	assert.True(t, j.inInbox("msg-cccccccccccc"))

	res = j.ack("../../etc/passwd")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "not a valid message id")
}

func TestMCPAck_RefusesWhatIsHeldBackAndAnEmptyOrOversizedBatch(t *testing.T) {
	j := newJoinedDemo(t)
	plantOverviewSession(t, j.e.dataDir, "escsecret", session.RoleEsc, "ESC-SECRET-NAME", "/repo/demo", "", "working")
	j.put("msg-aaaaaaaaaaaa", "escsecret", message.TypeNotify, "SECRET-BODY")

	res := j.ack("msg-aaaaaaaaaaaa")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "held back")
	assert.NotContains(t, res.Raw, "SECRET-BODY")
	assert.NotContains(t, res.Raw, "ESC-SECRET-NAME")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"), "Desktop cannot archive what it cannot see")

	assert.True(t, j.ack().IsError, "no ids")
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("msg-%012d", i)
	}
	big := j.ack(many...)
	require.True(t, big.IsError)
	assert.Contains(t, big.Text(), "at most 20")
}

// "The permission closes something": with the bridge closed, a message of that val is
// not Desktop's to handle, even by id and even though it was shown while it was open.
func TestMCPAck_AClosedBridgeMeansTheValsMessagesCannotBeAcknowledged(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "a tell")
	require.Contains(t, j.next(0).Text(), "a tell")
	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))

	res := j.ack("msg-aaaaaaaaaaaa")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "held back")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"))
}

// The same ack arriving twice at once is what a client that retries a call it never
// heard back from does (the relay gives up after 60 s while the server runs on). The
// project's mutex turns the second into "already archived" instead of a failed move.
func TestMCPAck_TheSameAckTwiceAtOnceNeverFailsTheSecond(t *testing.T) {
	j := newJoinedDemo(t)
	for round := 0; round < 15; round++ {
		id := fmt.Sprintf("msg-%012d", round)
		j.put(id, j.valSID, message.TypeNotify, "tell")
		const racers = 6
		var wg sync.WaitGroup
		results := make([]toolResult, racers)
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = j.c.call("ack", map[string]any{"ids": []string{id}})
			}()
		}
		wg.Wait()
		archived := 0
		for i, r := range results {
			require.False(t, r.IsError, "round %d racer %d: %s", round, i, r.Text())
			if strings.Contains(r.Text(), id+": archived") {
				archived++
			}
		}
		assert.Equal(t, 1, archived, "exactly one of them did the archiving")
		assert.True(t, j.archived(id))
	}
}

// --- read --------------------------------------------------------------------

func TestMCPRead_FollowingTheHintsRebuildsTheMessageExactly(t *testing.T) {
	j := newJoinedDemo(t)
	var sb strings.Builder
	for i := 0; sb.Len() < 60000; i++ {
		sb.WriteString("line \"quoted\" \\ é€𝄞 ")
		sb.WriteString(fmt.Sprint(i))
		sb.WriteString("\n\t")
	}
	body := sb.String()
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, body)

	var rebuilt strings.Builder
	offset, calls := 0, 0
	for {
		res := j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa", "offset": offset})
		require.False(t, res.IsError, res.Text())
		require.Len(t, res.Blocks, 2)
		require.LessOrEqual(t, len(res.Raw), j.app.cfg.MCPMaxResultBytes, "the whole response line fits the budget")
		head, piece, ok := strings.Cut(res.Blocks[0], "---\n")
		require.True(t, ok, "label, then the body")
		assert.Contains(t, head, "untrusted content below")
		require.True(t, utf8.ValidString(piece), "a piece never ends inside a character")
		rebuilt.WriteString(piece)
		calls++
		require.Less(t, calls, 100, "bounded")
		if strings.HasPrefix(res.Blocks[1], "End of the message") {
			break
		}
		var id string
		_, err := fmt.Sscanf(res.Blocks[1], "The message continues: call `read` with id %s and offset %d.", &id, &offset)
		require.NoError(t, err, res.Blocks[1])
	}
	assert.Equal(t, body, rebuilt.String())
	assert.Greater(t, calls, 2, "it really was in pieces")
}

func TestMCPRead_ShowsWhetherTheMessageIsArchivedAndStillReadsIt(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "to be put away")
	require.False(t, j.ack("msg-aaaaaaaaaaaa").IsError)

	res := j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa"})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "| archived |")
	assert.Contains(t, res.Text(), "to be put away")

	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeNotify, "still here")
	still := j.c.call("read", map[string]any{"id": "msg-bbbbbbbbbbbb"}).Text()
	assert.Contains(t, still, "| in your inbox |")
	assert.NotContains(t, still, "archived")
}

func TestMCPRead_RefusesWhatIsHeldBackIdsThatAreNotIdsAndOffsetsOutside(t *testing.T) {
	j := newJoinedDemo(t)
	plantOverviewSession(t, j.e.dataDir, "escsecret", session.RoleEsc, "ESC-SECRET-NAME", "/repo/demo", "", "working")
	j.put("msg-aaaaaaaaaaaa", "escsecret", message.TypeNotify, "SECRET-BODY")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeNotify, "short")

	res := j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "held back (author-not-val)")
	assert.NotContains(t, res.Raw, "SECRET-BODY")
	assert.NotContains(t, res.Raw, "ESC-SECRET-NAME")

	for _, id := range []string{"../../etc/passwd", "msg-", "MSG-AAAAAAAAAAAA", ""} {
		bad := j.c.call("read", map[string]any{"id": id})
		require.True(t, bad.IsError, "id %q", id)
		assert.Contains(t, bad.Text(), "not a valid message id", "id %q is refused as a grammar, before any path is built", id)
	}
	res = j.c.call("read", map[string]any{"id": "msg-cccccccccccc"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "no message with this id")

	assert.True(t, j.c.call("read", map[string]any{"id": "msg-bbbbbbbbbbbb", "offset": 6}).IsError, "past the end")
	assert.True(t, j.c.call("read", map[string]any{"id": "msg-bbbbbbbbbbbb", "offset": -1}).IsError)
	assert.False(t, j.c.call("read", map[string]any{"id": "msg-bbbbbbbbbbbb", "offset": 5}).IsError, "exactly the end is the empty tail")
}

// With the bridge closed a message that was readable a minute ago is not.
func TestMCPRead_AClosedBridgeClosesTheMessagesOfThatVal(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "VISIBLE-WHILE-OPEN")
	require.Contains(t, j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa"}).Text(), "VISIBLE-WHILE-OPEN")

	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))
	res := j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "author-not-open")
	assert.NotContains(t, res.Raw, "VISIBLE-WHILE-OPEN")
}

// An offset inside a character moves to the next one, and the header says where the
// piece really starts.
func TestMCPRead_AnOffsetInsideACharacterStartsAtTheNextOne(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "a€b") // a=1 byte, €=3 bytes, b=1
	res := j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa", "offset": 2})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Blocks[0], "content bytes 4-5 of 5")
	assert.True(t, strings.HasSuffix(res.Blocks[0], "---\nb"))
}

// The grant ending by itself closes the same doors as the val closing it: judged on
// the clock the manager keeps, not on whichever the tool happens to read.
func TestMCPAckAndRead_AnExpiredBridgeClosesThem(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "VISIBLE-WHILE-OPEN")
	j.e.clock.advance(9 * time.Hour) // the grant was 8 hours

	res := j.ack("msg-aaaaaaaaaaaa")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "held back")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"))

	res = j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "author-not-open")
	assert.NotContains(t, res.Raw, "VISIBLE-WHILE-OPEN")
}
