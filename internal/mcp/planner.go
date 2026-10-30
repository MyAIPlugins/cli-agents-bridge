package mcp

import (
	"sort"
	"sync"
)

// tailReserveRows is how many rows of the already-represented tail every result
// keeps room for, whenever such a tail exists. Without a reserve a steady arrival
// of new messages fills every result and the tail is never served (C4 of the
// design-gate: the ceil(N/K) bound only holds when the tail has a quota).
const tailReserveRows = 3

// Item is one candidate for a result: something in the inbox the agent has not
// finished with. Sizes are serialized bytes. CompactSize is the size of the
// one-line form (its id comes first, so the agent can always `read` it).
type Item struct {
	ID          string
	FullSize    int
	CompactSize int
}

// Entry is one row of a plan, in the order it must be rendered.
type Entry struct {
	ID   string
	Full bool // false: the compact one-line form
}

// Plan says what one result carries.
type Plan struct {
	Entries []Entry
	// Omitted counts the candidates that this result did not represent at all.
	Omitted int
}

// Planner decides, per result, which of the inbox's messages go out in full, which
// as a pointer line and which wait. It holds the only state the `next` projection
// needs: for each message, the last time a result REPRESENTED it. That state lives
// in memory and is lost with the process (measured: Desktop restarts the server on
// the next call after a crash), which is fine because nothing depends on it for
// correctness: it only orders the presentation. A message the planner forgot is
// simply new again and comes out in full.
//
// WHAT IS GUARANTEED, and what is not (the design-gate asked for exactly this):
//
//   - Emission fairness on a finite, stable set. Every id is represented within
//     ceil(N/K) results, K being the rows that fit after the reserve, because the
//     tail is served least-recently-represented first and each result serves at
//     least min(tailReserveRows, tail) rows of it.
//   - A message that arrives while no older message is waiting to be represented
//     is represented by the very next result (it is first among the new ones), as
//     long as one compact line fits in what the reserve leaves.
//   - Nothing else. A new message may have OLDER new ones ahead of it; "urgent" is
//     not a notion the planner has, and it adds no priority system. And no
//     rotation can guarantee RECEPTION: a result is emitted by the server, and the
//     relay may drop it. What the rotation guarantees is that an id whose result was
//     lost comes round again.
type Planner struct {
	mu  sync.Mutex
	seq uint64
	rep map[string]uint64 // id -> sequence number of the result that last represented it
}

// NewPlanner builds an empty planner.
func NewPlanner() *Planner { return &Planner{rep: map[string]uint64{}} }

// Plan chooses what the next result carries, within budget bytes, from items given
// OLDEST FIRST, and records what it chose as represented. Safe for concurrent use.
//
// Order of the result: the new messages, oldest first, each in full if it fits and
// as a pointer line if only that fits (a pointer counts as represented: it carries
// the id); then the tail, least recently represented first, as pointer lines. When
// a tail exists, the new ones may not take the last tailReserveRows rows' worth of
// the budget; whatever the tail leaves unused goes back to the new ones.
func (p *Planner) Plan(items []Item, budget int) Plan {
	p.mu.Lock()
	defer p.mu.Unlock()

	present := make(map[string]bool, len(items))
	for _, it := range items {
		present[it.ID] = true
	}
	for id := range p.rep { // forget what left the inbox, or the map grows forever
		if !present[id] {
			delete(p.rep, id)
		}
	}

	var fresh, tail []Item
	for _, it := range items {
		if _, seen := p.rep[it.ID]; seen {
			tail = append(tail, it)
		} else {
			fresh = append(fresh, it)
		}
	}
	sort.SliceStable(tail, func(i, j int) bool { return p.rep[tail[i].ID] < p.rep[tail[j].ID] })

	reserve := reserveFor(tail, budget)
	var entries []Entry
	used := 0
	nextFresh := 0

	// New messages, within what the reserve leaves.
	nextFresh, entries, used = takeFresh(fresh, nextFresh, entries, used, budget-reserve)
	// The tail, within what is left of the whole budget.
	for _, it := range tail {
		if used+it.CompactSize > budget {
			break
		}
		entries = append(entries, Entry{ID: it.ID})
		used += it.CompactSize
	}
	// Room the tail did not use goes back to the new ones, still in order.
	_, entries, _ = takeFresh(fresh, nextFresh, entries, used, budget)

	p.seq++
	for _, e := range entries {
		p.rep[e.ID] = p.seq
	}
	return Plan{Entries: entries, Omitted: len(items) - len(entries)}
}

// takeFresh appends new messages starting at index from, as long as they fit in
// limit bytes of total use. It stops at the first one that does not fit even as a
// pointer: skipping ahead would let small messages overtake a big one forever.
func takeFresh(fresh []Item, from int, entries []Entry, used, limit int) (int, []Entry, int) {
	i := from
	for ; i < len(fresh); i++ {
		it := fresh[i]
		switch {
		case used+it.FullSize <= limit:
			entries = append(entries, Entry{ID: it.ID, Full: true})
			used += it.FullSize
		case used+it.CompactSize <= limit:
			entries = append(entries, Entry{ID: it.ID})
			used += it.CompactSize
		default:
			return i, entries, used
		}
	}
	return i, entries, used
}

// reserveFor is the part of the budget kept for the tail: the compact size of its
// first tailReserveRows rows, never more than half of the budget.
func reserveFor(tail []Item, budget int) int {
	reserve := 0
	for i := 0; i < len(tail) && i < tailReserveRows; i++ {
		reserve += tail[i].CompactSize
	}
	if reserve > budget/2 {
		reserve = budget / 2
	}
	return reserve
}

// Tracked is how many ids the planner currently remembers, for tests and
// diagnostics.
func (p *Planner) Tracked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.rep)
}
