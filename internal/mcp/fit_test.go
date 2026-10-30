package mcp

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFitResponse_LeavesASmallResultAlone(t *testing.T) {
	t.Parallel()
	line, cut, err := fitResponse(json.RawMessage("1"), Result{Content: []Block{Text("hello")}}, 2048)
	require.NoError(t, err)
	assert.False(t, cut)
	assert.Contains(t, string(line), "hello")
	assert.NotContains(t, string(line), "cut by the server")
}

func TestFitResponse_CutsABigResultAndSaysSo(t *testing.T) {
	t.Parallel()
	line, cut, err := fitResponse(json.RawMessage(`"abc"`), Result{Content: []Block{Text(strings.Repeat("x", 50000))}}, 2048)
	require.NoError(t, err)
	assert.True(t, cut)
	assert.LessOrEqual(t, len(line), 2048)
	assert.Contains(t, string(line), "result cut by the server", "a cut result always carries the notice")
	assert.True(t, json.Valid(line))
}

func TestFitResponse_NeverCutsInTheMiddleOfARune(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("è☃𝄞", 3000) // 2-, 3- and 4-byte runes
	line, _, err := fitResponse(json.RawMessage("7"), Result{Content: []Block{Text(body)}}, 1500)
	require.NoError(t, err)
	var resp struct {
		Result Result `json:"result"`
	}
	require.NoError(t, json.Unmarshal(line, &resp))
	for _, b := range resp.Result.Content {
		assert.True(t, utf8.ValidString(b.Text))
		// encoding/json replaces an invalid byte with U+FFFD, so a cut through the
		// middle of a rune shows up as one; the body contains none of its own.
		assert.False(t, strings.ContainsRune(b.Text, utf8.RuneError), "a cut must land on a rune boundary")
	}
}

func TestFitResponse_KeepsTheErrorFlag(t *testing.T) {
	t.Parallel()
	line, cut, err := fitResponse(json.RawMessage("1"), ErrorResult(strings.Repeat("e", 9000)), 1024)
	require.NoError(t, err)
	require.True(t, cut)
	assert.Contains(t, string(line), `"isError":true`)
}

func TestFitResponse_ShrinksTheBigBlockNotTheSmallOnes(t *testing.T) {
	t.Parallel()
	res := Result{Content: []Block{Text("header line"), Text(strings.Repeat("b", 30000)), Text("footer line")}}
	line, cut, err := fitResponse(json.RawMessage("1"), res, 2048)
	require.NoError(t, err)
	require.True(t, cut)
	assert.Contains(t, string(line), "header line")
	assert.Contains(t, string(line), "footer line")
}

// The promise is "no response is larger than the budget", for ANY content. Quotes,
// newlines and non-ASCII inflate the JSON beyond the raw text, so the property is
// checked on the serialized line, over random sizes, alphabets and block counts.
func TestFitResponse_PropertyTheLineNeverExceedsTheBudget(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(7))
	alphabets := []string{"abc", "\"\\\n\t", "è☃𝄞", "x"}
	for i := 0; i < 400; i++ {
		max := minResultBytes + rng.Intn(6000)
		var blocks []Block
		for b := 0; b < 1+rng.Intn(5); b++ {
			alpha := []rune(alphabets[rng.Intn(len(alphabets))])
			var sb strings.Builder
			for n := rng.Intn(20000); n > 0; n-- {
				sb.WriteRune(alpha[rng.Intn(len(alpha))])
			}
			blocks = append(blocks, Text(sb.String()))
		}
		line, _, err := fitResponse(json.RawMessage("12345"), Result{Content: blocks, IsError: rng.Intn(2) == 0}, max)
		require.NoError(t, err)
		require.LessOrEqual(t, len(line), max, "iteration %d: %d blocks, budget %d", i, len(blocks), max)
		require.True(t, json.Valid(line))
	}
}

func TestServer_EveryToolResultIsFittedToTheBudget(t *testing.T) {
	c := newTestClient(t, nil) // MaxResultBytes 2048
	c.init()
	c.send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"big","arguments":{"n":400000}}}`)
	r := c.recv()
	assert.LessOrEqual(t, r["_rawlen"].(float64), float64(2048), "the line on the wire respects the budget")
	text, _ := resultText(t, r)
	assert.Contains(t, text, "result cut by the server")
}
