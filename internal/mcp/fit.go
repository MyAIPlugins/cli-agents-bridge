package mcp

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// minResultBytes is the smallest budget the server accepts. Below it the envelope
// of a response and the truncation notice would not fit, and a budget that cannot
// be honoured is refused when the server is built, not at the first big result.
const minResultBytes = 512

// maxShrinkRounds bounds the loop that shrinks a result until it fits. Each round
// cuts a tenth of what is left, so 64 rounds bring any text below one byte.
const maxShrinkRounds = 64

// truncationNotice is appended to a result that had to be cut. A client that
// sees a result without it knows the content is whole.
func truncationNotice(max int) string {
	return fmt.Sprintf("[result cut by the server to fit %d bytes: what precedes this line is incomplete]", max)
}

// fitResponse serializes the response for id and guarantees that the line, as it
// goes on the wire, is at most max bytes. It is the safety net for EVERY tool:
// the bridge-aware projection (which decides WHAT to drop) lives with the tools,
// and this only makes the promise "no result is larger than the budget" true even
// for a tool that forgot, or for an error text that grew.
//
// It reports whether it had to cut. The cut is made on the text blocks in order,
// at a rune boundary, and a notice block is always appended to a cut result.
func fitResponse(id json.RawMessage, res Result, max int) (line []byte, cut bool, err error) {
	line, err = marshalResult(id, res)
	if err != nil {
		return nil, false, err
	}
	if len(line) <= max {
		return line, false, nil
	}

	notice := Text(truncationNotice(max))
	blocks := append([]Block(nil), res.Content...)
	for round := 0; round < maxShrinkRounds; round++ {
		blocks = shrinkBlocks(blocks, max, len(line))
		candidate := Result{Content: append(append([]Block(nil), blocks...), notice), IsError: res.IsError}
		line, err = marshalResult(id, candidate)
		if err != nil {
			return nil, false, err
		}
		if len(line) <= max {
			return line, true, nil
		}
	}
	// Last resort, still honest: nothing but the notice. It fits because max is
	// at least minResultBytes and the envelope plus the notice is far below it.
	only := Result{Content: []Block{notice}, IsError: res.IsError}
	line, err = marshalResult(id, only)
	if err != nil {
		return nil, false, err
	}
	if len(line) > max {
		return nil, false, fmt.Errorf("mcp: even the truncation notice does not fit in %d bytes", max)
	}
	return line, true, nil
}

// shrinkBlocks cuts about a tenth off the longest text, keeping a rune boundary.
// Working on the longest block first means a result made of one big body and a
// few small lines loses the body, not the lines.
func shrinkBlocks(blocks []Block, max, currentLine int) []Block {
	if len(blocks) == 0 {
		return blocks
	}
	longest := 0
	for i, b := range blocks {
		if len(b.Text) > len(blocks[longest].Text) {
			longest = i
		}
	}
	text := blocks[longest].Text
	excess := currentLine - max
	cutBy := len(text) / 10
	if cutBy < excess {
		cutBy = excess
	}
	keep := len(text) - cutBy
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(text[keep]) {
		keep--
	}
	out := append([]Block(nil), blocks...)
	out[longest] = Block{Type: out[longest].Type, Text: text[:keep]}
	return out
}

func marshalResult(id json.RawMessage, res Result) ([]byte, error) {
	if res.Content == nil {
		res.Content = []Block{}
	}
	line, err := json.Marshal(response{JSONRPC: jsonRPCVersion, ID: id, Result: res})
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal result: %w", err)
	}
	return line, nil
}
