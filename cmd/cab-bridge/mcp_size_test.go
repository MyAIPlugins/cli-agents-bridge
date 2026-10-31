//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// "N bytes" in the header of a message is the size of its TEXT, the number `read`'s
// "content bytes a-b of N" counts in and the CLI's `[83 B]` shows. A real Cowork saw
// 668 for a text of 83: the size of the JSON file, envelope included.

var (
	headerBytes = regexp.MustCompile(`\| (\d+) bytes\b`)
	readTotal   = regexp.MustCompile(`content bytes \d+-\d+ of (\d+)`)
)

// fileSizeOf is the size of the message file in the inbox: the number the header used
// to show. Asserting it differs from the text's is what keeps these tests honest.
func fileSizeOf(t *testing.T, j *joinedDemo, id string) int {
	t.Helper()
	st, err := os.Stat(filepath.Join(j.inbox, id+".json"))
	require.NoError(t, err)
	return int(st.Size())
}

func headerSize(t *testing.T, text string) int {
	t.Helper()
	m := headerBytes.FindStringSubmatch(text)
	require.NotNil(t, m, "the header carries a size: %q", text)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

func TestMCPNext_TheHeaderCountsTheBytesOfTheTextNotOfTheFile(t *testing.T) {
	j := newJoinedDemo(t)
	body := "caffè e latte" // 13 characters, 14 bytes: counting characters is not counting bytes
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, body)
	require.NotEqual(t, len(body), fileSizeOf(t, j, "msg-aaaaaaaaaaaa"), "the premise: the file is bigger than its text")
	require.NotEqual(t, len([]rune(body)), len(body), "and the text has a character of two bytes")

	full := j.next(0)
	require.False(t, full.IsError, full.Text())
	assert.Equal(t, len(body), headerSize(t, full.Text()), "in full")
	assert.NotContains(t, full.Text(), fmt.Sprintf("| %d bytes", fileSizeOf(t, j, "msg-aaaaaaaaaaaa")))

	pointer := j.next(0)
	assert.Equal(t, len(body), headerSize(t, pointer.Text()), "and as the pointer line of the next call")
}

func TestMCPNext_ATooLargeMessageIsMeasuredInItsTextToo(t *testing.T) {
	j := newJoinedDemo(t)
	body := strings.Repeat("x", 2*j.app.cfg.MCPMaxResultBytes) + "é"
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, body)
	require.NotEqual(t, len(body), fileSizeOf(t, j, "msg-aaaaaaaaaaaa"))

	res := j.next(0)
	require.False(t, res.IsError, res.Text())
	require.Contains(t, res.Text(), "too large to show here", "it took the pointer branch")
	assert.Equal(t, len(body), headerSize(t, res.Text()))
}

func TestMCPRead_TheHeaderSaysTheSameNumberOfBytesAsTheRangeItIsReading(t *testing.T) {
	j := newJoinedDemo(t)
	body := strings.Repeat("é", 12000) // 24000 bytes: two pieces
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, body)
	require.NotEqual(t, len(body), fileSizeOf(t, j, "msg-aaaaaaaaaaaa"))

	for _, offset := range []int{0, 18432} {
		res := j.c.call("read", map[string]any{"id": "msg-aaaaaaaaaaaa", "offset": offset})
		require.False(t, res.IsError, res.Text())
		total := readTotal.FindStringSubmatch(res.Blocks[0])
		require.NotNil(t, total, res.Blocks[0])
		assert.Equal(t, strconv.Itoa(len(body)), total[1], "offset %d: the range is out of the text's bytes", offset)
		assert.Equal(t, len(body), headerSize(t, res.Blocks[0]), "offset %d: and the header's size is the same number, not another thing called bytes", offset)
	}
}
