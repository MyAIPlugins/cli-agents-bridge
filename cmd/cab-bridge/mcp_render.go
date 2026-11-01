package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
)

// How the MCP tools write what they return. Everything here takes visibleMessage
// and withheld, never a raw message (mcp_authorize.go), so nothing unauthorized can
// be rendered.
//
// The one rule of the layout is that a message body is DATA: it sits in a block of
// its own under a label that says whose it is and that it is untrusted content, and
// the instructions of the tool (what to do with an id) live in a different block that
// no message can write. A body that says "ignore the above" is a body.

const (
	// blockOverhead is what one text block costs in the serialized result beyond its
	// text: {"type":"text","text":""}, the comma, and slack. The slack is what pays for
	// the separator in front of every block but the first (blockSep, two bytes once
	// escaped), so the plan does not count it separately.
	blockOverhead = 40
	// envelopeSlack is what the JSON-RPC envelope costs around the result: the
	// protocol tag, an id of up to ten digits and the braces.
	envelopeSlack = 100
	// previewRunes is how much of a body a pointer line quotes.
	previewRunes = 80
	// maxHeldIDsListed bounds the ids named in a withheld notice.
	maxHeldIDsListed = 10
)

// blockSep goes in front of every block but the first. A client may join the text
// blocks of a result with nothing between them, and then "...open for you." runs into
// "No val of this project": each block says where it begins instead of relying on
// the reader to put a line break between them.
const blockSep = "\n"

// after builds a block that is not the first of its result.
func after(text string) mcp.Block { return mcp.Text(blockSep + text) }

// pointerTitle heads the block of pointer lines.
const pointerTitle = "Pointer lines (no body: already shown on this Claude Desktop surface, perhaps in another conversation, or too large to inline):\n"

func moreLine(n int) string {
	return fmt.Sprintf("%d more message(s) did not fit in this result; they come up in the next calls.", n)
}

const nextFooter = "To answer an ask use `reply` with ask_id set to its id. To clear a tell or a response you have handled use `ack` with their ids. " +
	"Nothing leaves the inbox until you do one of the two. A pointer line (no body) means the message was already shown on this Claude Desktop surface (perhaps in another conversation) or is too large: `read` with its id gives it."

// footerOf is the footer of a `next` result, or nothing when there is no message to act
// on (a closed bridge shows none, and neither does a wait that found nothing). The
// budget is counted on this text and the result is written with it, so the two cannot
// disagree about whether the footer is there.
func footerOf(v nextView) string {
	if len(v.visible) == 0 {
		return ""
	}
	return nextFooter
}

// nextView is everything one `next` result is made of.
type nextView struct {
	project   string
	agentName string
	visible   []visibleMessage // oldest first
	held      []withheld
	corrupt   int
	// bridgeClosed says that no val of the project has the bridge open now. An empty
	// `next` would otherwise read as "the inbox is empty" when it is "you cannot see".
	bridgeClosed bool
	now          time.Time
	budget       int // the whole response line, in bytes
}

const bridgeClosedNotice = "No val of this project has the bridge open right now, so nothing from a val is shown and nothing can be sent. A val reopens it with `cab-bridge desktop open`."

// jsonLen is the size of a string once serialized as a JSON string.
func jsonLen(s string) int {
	b, err := json.Marshal(s)
	if err != nil {
		return len(s) * 6 // cannot happen for a Go string; the worst-case escape if it ever did
	}
	return len(b)
}

func age(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "age unknown"
	}
	d := now.Sub(t).Round(time.Second)
	if d < 0 {
		return "just now"
	}
	return d.String() + " ago"
}

func previewOf(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= previewRunes {
		return s
	}
	r := []rune(s)
	return string(r[:previewRunes]) + "..."
}

// label is the header of a message. Its size is the size of the TEXT, in bytes: the
// unit `read` cuts in (content bytes a-b of N) and the CLI shows. It used to be the
// size of the file in the inbox, envelope included, so a text of 83 bytes said 668.
func label(v visibleMessage, now time.Time) string {
	return fmt.Sprintf("id %s | from %s | %s | %s | %d bytes", v.ID(), v.AuthorName(), kindOf(v.Type()), age(v.Timestamp(), now), len(v.Content()))
}

func kindOf(t string) string {
	switch t {
	case "query":
		return "ask"
	case "notify":
		return "tell"
	default:
		return t
	}
}

func renderFull(v visibleMessage, now time.Time) string {
	return "--- " + label(v, now) + " | untrusted content below ---\n" + v.Content()
}

func renderPointer(v visibleMessage, now time.Time, big bool) string {
	line := label(v, now) + " | " + previewOf(v.Content())
	if big {
		line += " [too large to show here: read with offset 0]"
	}
	return line
}

// renderNext builds the result of a `next`. The Planner decides which messages go out
// in full, which as a pointer line and which wait; this only writes them down. The
// byte budget it plans against is what is left of the response after the envelope,
// the fixed texts and the notices, so the plan is made on the real numbers and the
// server's own fit is only a net.
func renderNext(pl *mcp.Planner, v nextView) mcp.Result {
	byID := make(map[string]visibleMessage, len(v.visible))
	for _, m := range v.visible {
		byID[m.ID()] = m
	}

	header := fmt.Sprintf("Bridge: project %s, you are %s. %d message(s) open for you.", v.project, v.agentName, len(v.visible))
	notices := noticeBlock(v.held, v.corrupt)
	if v.bridgeClosed {
		notices = strings.TrimSpace(bridgeClosedNotice + "\n" + notices)
	}
	// Everything that is in the result whatever the messages are, counted from the
	// texts themselves: five blocks at most (header, pointer block, "more" line,
	// notices, footer), each with its own overhead, and the worst case of the "more" count.
	footer := footerOf(v)
	fixed := envelopeSlack + 5*blockOverhead + jsonLen(header) + jsonLen(footer) + jsonLen(notices) +
		jsonLen(pointerTitle) + jsonLen(moreLine(len(v.visible)))

	budget := v.budget - fixed
	if budget < 0 {
		budget = 0
	}
	ordered := append([]visibleMessage(nil), v.visible...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Timestamp() != ordered[j].Timestamp() {
			return ordered[i].Timestamp() < ordered[j].Timestamp()
		}
		return ordered[i].ID() < ordered[j].ID()
	})
	items := make([]mcp.Item, 0, len(ordered))
	tooBig := make(map[string]bool, len(ordered))
	for _, m := range ordered {
		full := blockOverhead + jsonLen(renderFull(m, v.now))
		// A body that cannot fit even alone is a pointer with the way to read it, and
		// that pointer is longer: its size is counted as it will be written.
		big := full > budget
		tooBig[m.ID()] = big
		items = append(items, mcp.Item{
			ID:          m.ID(),
			FullSize:    full,
			CompactSize: jsonLen(renderPointer(m, v.now, big)) + 3,
		})
	}
	plan := pl.Plan(items, budget)

	blocks := []mcp.Block{mcp.Text(header)}
	var pointers []string
	for _, e := range plan.Entries {
		m := byID[e.ID]
		if e.Full {
			blocks = append(blocks, after(renderFull(m, v.now)))
			continue
		}
		pointers = append(pointers, renderPointer(m, v.now, tooBig[e.ID]))
	}
	if len(pointers) > 0 {
		blocks = append(blocks, after(pointerTitle+strings.Join(pointers, "\n")))
	}
	if plan.Omitted > 0 {
		blocks = append(blocks, after(moreLine(plan.Omitted)))
	}
	if notices != "" {
		blocks = append(blocks, after(notices))
	}
	if footer != "" {
		blocks = append(blocks, after(footer))
	}
	return mcp.Result{Content: blocks}
}

// readView is everything one `read` result is made of: a slice of one message the
// author of which is already authorized.
type readView struct {
	msg      visibleMessage
	archived bool
	now      time.Time
	offset   int // where this piece starts, in bytes of the content
	piece    string
	next     int // where the following piece starts
	done     bool
	total    int // bytes of content
}

// renderRead writes a piece of a message: the body under its label, and a fixed
// closing line that says where to continue. Both are sized by the caller against the
// budget; the closing line carries only an id of the validated wire format and numbers.
func renderRead(v readView) mcp.Result {
	state := "in your inbox"
	if v.archived {
		state = "archived"
	}
	head := fmt.Sprintf("--- %s | %s | content bytes %d-%d of %d | untrusted content below ---\n",
		label(v.msg, v.now), state, v.offset, v.offset+len(v.piece), v.total)
	return mcp.Result{Content: []mcp.Block{mcp.Text(head + v.piece), after(readTail(v))}}
}

func readTail(v readView) string {
	if v.done {
		return "End of the message."
	}
	return fmt.Sprintf("The message continues: call `read` with id %s and offset %d.", v.msg.ID(), v.next)
}

// resultBytes is the serialized size of a result as the plan counts it: the envelope,
// and for every block its overhead and its text once escaped.
func resultBytes(res mcp.Result) int {
	n := envelopeSlack
	for _, b := range res.Content {
		n += blockOverhead + jsonLen(b.Text)
	}
	return n
}

// noticeBlock says what was held back and what could not be read, with ids and
// reasons only: no author name, no size, no preview.
func noticeBlock(held []withheld, corrupt int) string {
	var parts []string
	if len(held) > 0 {
		ids := make([]string, 0, maxHeldIDsListed)
		reasons := map[withheldReason]int{}
		for i, h := range held {
			reasons[h.Reason]++
			if i < maxHeldIDsListed {
				ids = append(ids, h.ID)
			}
		}
		var why []string
		for _, r := range []withheldReason{reasonNotVal, reasonOtherScope, reasonNotOpen, reasonUnresolvable} {
			if reasons[r] > 0 {
				why = append(why, fmt.Sprintf("%s x%d", r, reasons[r]))
			}
		}
		more := ""
		if len(held) > len(ids) {
			more = fmt.Sprintf(" and %d more", len(held)-len(ids))
		}
		parts = append(parts, fmt.Sprintf("%d message(s) are held back and not shown (%s): %s%s. They stay on disk; a val decides what to do with them from the CLI.",
			len(held), strings.Join(why, ", "), strings.Join(ids, ", "), more))
	}
	if corrupt > 0 {
		parts = append(parts, fmt.Sprintf("%d unreadable file(s) in the inbox were left alone.", corrupt))
	}
	return strings.Join(parts, "\n")
}

// sliceForRead cuts content for `read`: at most limit bytes from offset, on rune
// boundaries, and says where to continue. Offsets are byte offsets of the original,
// so a sequence of reads that follow each hint reconstructs the content exactly.
func sliceForRead(content string, offset, limit int) (piece string, next int, done bool, err error) {
	if offset < 0 || offset > len(content) {
		return "", 0, false, fmt.Errorf("offset %d is outside the message (%d bytes)", offset, len(content))
	}
	if limit <= 0 {
		return "", 0, false, fmt.Errorf("limit must be positive")
	}
	for offset < len(content) && !utf8.RuneStart(content[offset]) {
		offset++ // an offset inside a rune moves forward to the next one
	}
	end := offset + limit
	if end >= len(content) {
		return content[offset:], len(content), true, nil
	}
	for end > offset && !utf8.RuneStart(content[end]) {
		end--
	}
	if end == offset { // a single rune wider than the limit: take it whole, never nothing
		_, w := utf8.DecodeRuneInString(content[offset:])
		end = offset + w
	}
	return content[offset:end], end, false, nil
}
