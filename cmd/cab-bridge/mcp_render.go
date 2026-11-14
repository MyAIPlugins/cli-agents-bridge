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
	// unreadable are files the system would not let be read, named in the notice with the
	// cause (F-172). corrupt is only a count: those were read and are not messages.
	unreadable []unreadableFile
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

// tooLargeSuffix ends the pointer line of a body that cannot fit even alone.
const tooLargeSuffix = " [too large to show here: read with offset 0]"

func renderPointer(v visibleMessage, now time.Time, big bool) string {
	line := label(v, now) + " | " + previewOf(v.Content())
	if big {
		line += tooLargeSuffix
	}
	return line
}

// renderNext builds the result of a `next`. The Planner decides which messages go out
// in full, which as a pointer line and which wait; this only writes them down. The
// byte budget it plans against is what is left of the response after the envelope,
// the fixed texts and the notices, so the plan is made on the real numbers and the
// server's own fit is only a net.
func renderNext(pl *mcp.Planner, v nextView) mcp.Result {
	return renderWithNotices(pl, v, fittedNotices(v))
}

// renderWithNotices writes the result of a `next` with the notices as given: renderNext
// gives it the ones fittedNotices chose, and a test gives it each form in turn.
func renderWithNotices(pl *mcp.Planner, v nextView, notices string) mcp.Result {
	byID := make(map[string]visibleMessage, len(v.visible))
	for _, m := range v.visible {
		byID[m.ID()] = m
	}

	header := headerOf(v)
	footer := footerOf(v)
	budget := v.budget - fixedBytes(v) - jsonLen(notices)
	if budget < 0 {
		budget = 0
	}
	items, tooBig := planItems(sizedOldestFirst(v), budget)
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

// headerOf is the first line of a `next` result.
func headerOf(v nextView) string {
	return fmt.Sprintf("Bridge: project %s, you are %s. %d message(s) open for you.", v.project, v.agentName, len(v.visible))
}

// fixedBytes is everything in the result whatever the messages and the notices are,
// counted from the texts themselves: five blocks at most (header, pointer block, "more"
// line, notices, footer), each with its own overhead, and the worst case of the "more"
// count.
func fixedBytes(v nextView) int {
	return envelopeSlack + 5*blockOverhead + jsonLen(headerOf(v)) + jsonLen(footerOf(v)) +
		jsonLen(pointerTitle) + jsonLen(moreLine(len(v.visible)))
}

// oldestFirst is the order the plan takes the messages in.
func oldestFirst(msgs []visibleMessage) []visibleMessage {
	ordered := append([]visibleMessage(nil), msgs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Timestamp() != ordered[j].Timestamp() {
			return ordered[i].Timestamp() < ordered[j].Timestamp()
		}
		return ordered[i].ID() < ordered[j].ID()
	})
	return ordered
}

// sizedMessage is a message with the bytes it takes written in full, as a pointer line,
// and as the longer pointer line a body that cannot fit even alone gets. Reading a body to
// count it is the expensive part of a result, so it is done once per result and not once
// per form of the notice.
type sizedMessage struct {
	msg        visibleMessage
	full       int
	pointer    int
	bigPointer int
}

// sizedOldestFirst measures the messages, in the order the plan takes them.
func sizedOldestFirst(v nextView) []sizedMessage {
	ordered := oldestFirst(v.visible)
	sized := make([]sizedMessage, len(ordered))
	for i, m := range ordered {
		line := renderPointer(m, v.now, false)
		sized[i] = sizedMessage{
			msg:        m,
			full:       blockOverhead + jsonLen(renderFull(m, v.now)),
			pointer:    jsonLen(line) + 3,
			bigPointer: jsonLen(line+tooLargeSuffix) + 3,
		}
	}
	return sized
}

// planItems describes the messages to the plan for room bytes, and says which of them
// are pointers with the way to read them. A body that cannot fit even alone is that
// pointer, and the pointer is longer: its size is counted as it will be written.
func planItems(sized []sizedMessage, room int) ([]mcp.Item, map[string]bool) {
	items := make([]mcp.Item, 0, len(sized))
	tooBig := make(map[string]bool, len(sized))
	for _, sm := range sized {
		big := sm.full > room
		tooBig[sm.msg.ID()] = big
		compact := sm.pointer
		if big {
			compact = sm.bigPointer
		}
		items = append(items, mcp.Item{ID: sm.msg.ID(), FullSize: sm.full, CompactSize: compact})
	}
	return items, tooBig
}

// presentedWithin is how many messages a plan with no history presents in room bytes:
// what the first result of a call shows, and the later ones rotate through the same room.
// It is the plan itself that answers, and not a count of pointer lines, because the plan
// writes the first messages in full when they fit and so spends room a pointer would not:
// more room does not always mean more messages. It is -1 when room is negative, the
// notices alone being over the budget.
func presentedWithin(sized []sizedMessage, room int) int {
	if room < 0 {
		return -1
	}
	items, _ := planItems(sized, room)
	return len(mcp.NewPlanner().Plan(items, room).Entries)
}

// noticeFit is one form of the notice of the unreadable files, written with the others,
// and what it leaves the messages.
type noticeFit struct {
	text      string
	room      int // bytes left for the messages
	presented int // messages a plan with no history presents in room, -1 when room is negative
}

// beats reports whether f is a better choice than the best so far: more messages
// presented; and when no message is presented at all, the shorter text, which leaves
// the most room. At equal count the earlier form stays, and the earlier is the fuller.
func (f noticeFit) beats(best noticeFit) bool {
	if f.presented != best.presented {
		return f.presented > best.presented
	}
	return f.presented <= 0 && f.room > best.room
}

// fittedNotices is every notice of the result in one text, with the notice of the
// unreadable files in the form that lets the most messages be presented, the fullest of
// them when several do. That notice is the one with no size of its own (its names are up
// to 255 bytes each); the others are bounded by construction (ten ids at most, fixed
// sentences) and are counted as they are. The notices must never be what keeps a
// readable message from being presented (F-172, CRI), and what each form leaves is
// counted on the text that will be written, separators included, by asking the plan what
// it would present in the room left. The forms are not ordered by size (one short name
// with a short cause is shorter than the form with no names), so nothing here takes a
// form for the shortest because of its place.
func fittedNotices(v nextView) string {
	forms := unreadableNotices(v.unreadable)
	if len(forms) == 0 {
		return noticesText(v, "")
	}
	bare, sized := fixedBytes(v), sizedOldestFirst(v)
	var best noticeFit
	for i, form := range forms {
		text := noticesText(v, form)
		room := v.budget - bare - jsonLen(text)
		fit := noticeFit{text: text, room: room, presented: presentedWithin(sized, room)}
		if i == 0 || fit.beats(best) {
			best = fit
		}
		if fit.presented == len(v.visible) {
			break // every message is presented: a shorter form adds nothing
		}
	}
	return best.text
}

// noticesText joins the notices of a result: the bridge-closed one, then the held-back
// one, then unreadable (already written, in one of its forms) and corrupt files.
func noticesText(v nextView, unreadable string) string {
	text := noticeBlock(v.held, v.corrupt, unreadable)
	if v.bridgeClosed {
		text = strings.TrimSpace(bridgeClosedNotice + "\n" + text)
	}
	return text
}

// noticeBlock says what was held back and what could not be delivered, with ids,
// causes and counts only: no author name, no size, no preview. unreadable is the notice
// of the unreadable files already written, or empty.
func noticeBlock(held []withheld, corrupt int, unreadable string) string {
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
	if unreadable != "" {
		parts = append(parts, unreadable)
	}
	if corrupt > 0 {
		parts = append(parts, corruptNotice(corrupt))
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
