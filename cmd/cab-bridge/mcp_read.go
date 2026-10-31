package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"unicode/utf8"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
)

// The `read` tool: one message by id, in pieces that fit a result.
//
// It finds the message in the inbox or among the archived ones, judges its author like
// every other door (a held message is refused, with the reason and nothing else), and
// cuts the text on rune boundaries so that following the closing line of each piece
// reconstructs the message exactly. The cut is sized on the serialized result, escapes
// included, so a body made of quotes and newlines still fits.

// maxReadRounds bounds the search for a piece size that fits: each round shrinks the
// candidate by a quarter, so forty rounds take any realistic budget below one byte.
const maxReadRounds = 40

type readArgs struct {
	Project string `json:"project"`
	ID      string `json:"id"`
	Offset  int    `json:"offset"`
}

func (a *mcpApp) toolRead(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args readArgs
	if err := mcp.DecodeArgs(raw, &args); err != nil {
		return mcp.Result{}, err
	}
	p, err := a.projectFor(args.Project)
	if err != nil {
		return mcp.Result{}, err
	}
	loc, err := locateMessage(filepath.Join(a.cfg.DataDir, "sessions", p.sid), args.ID)
	switch {
	case errors.Is(err, errMessageNotFound):
		return mcp.Result{}, mcp.Refuse("there is no message with this id in your inbox or among the archived ones")
	case err != nil:
		return mcp.Result{}, mcp.Refuse("this is not a valid message id")
	}
	m, err := loadMessageAt(loc.path, a.cfg.MaxMessageBytes)
	if err != nil {
		return mcp.Result{}, mcp.Refuse("the message could not be read")
	}
	v, held := newAuthorizer(a.mgr, p.scope, a.mgr.NowUTC()).authorize(m)
	if held != nil {
		return mcp.Result{}, mcp.Refusef("this message is held back (%s) and cannot be read from here", held.Reason)
	}
	return a.readPiece(v, loc.archived, args.Offset)
}

// readPiece cuts the largest piece from offset whose result fits the budget.
func (a *mcpApp) readPiece(v visibleMessage, archived bool, offset int) (mcp.Result, error) {
	content := v.Content()
	if offset < 0 || offset > len(content) {
		return mcp.Result{}, mcp.Refusef("offset %d is outside the message: its content is %d bytes", offset, len(content))
	}
	start := alignToRune(content, offset)
	limit := a.cfg.MCPMaxResultBytes
	for round := 0; round < maxReadRounds && limit > 0; round++ {
		piece, next, done, err := sliceForRead(content, start, limit)
		if err != nil {
			return mcp.Result{}, err
		}
		res := renderRead(readView{msg: v, archived: archived, now: a.mgr.NowUTC(), offset: start, piece: piece, next: next, done: done, total: len(content)})
		if resultBytes(res) <= a.cfg.MCPMaxResultBytes {
			return res, nil
		}
		limit = limit * 3 / 4
	}
	return mcp.Result{}, mcp.Refuse("not even one character of this message fits in a result with the configured limit")
}

// alignToRune moves an offset that falls inside a character to the next one, which is
// where sliceForRead starts cutting.
func alignToRune(content string, offset int) int {
	for offset < len(content) && !utf8.RuneStart(content[offset]) {
		offset++
	}
	return offset
}
