package mcp

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testFull    = 400
	testCompact = 100
)

func items(prefix string, n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{ID: fmt.Sprintf("%s%04d", prefix, i), FullSize: testFull, CompactSize: testCompact}
	}
	return out
}

func planSize(p Plan, byID map[string]Item) int {
	total := 0
	for _, e := range p.Entries {
		if e.Full {
			total += byID[e.ID].FullSize
		} else {
			total += byID[e.ID].CompactSize
		}
	}
	return total
}

func index(items []Item) map[string]Item {
	m := make(map[string]Item, len(items))
	for _, it := range items {
		m[it.ID] = it
	}
	return m
}

func TestPlanner_NewMessagesGoOutInFullOldestFirst(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	in := items("m", 3)
	plan := pl.Plan(in, 10000)
	require.Len(t, plan.Entries, 3)
	for i, e := range plan.Entries {
		assert.Equal(t, in[i].ID, e.ID, "oldest first")
		assert.True(t, e.Full, "a new message that fits goes out in full")
	}
	assert.Zero(t, plan.Omitted)
}

func TestPlanner_AMessageSeenOnceComesBackAsAPointerLine(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	in := items("m", 2)
	pl.Plan(in, 10000)
	second := pl.Plan(in, 10000)
	require.Len(t, second.Entries, 2)
	for _, e := range second.Entries {
		assert.False(t, e.Full, "already represented: the id and nothing more, `read` brings the rest")
	}
}

func TestPlanner_ABodyTooBigForTheBudgetStillGoesOutAsAPointer(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	in := []Item{{ID: "big", FullSize: 90000, CompactSize: 120}}
	plan := pl.Plan(in, 1000)
	require.Len(t, plan.Entries, 1)
	assert.False(t, plan.Entries[0].Full)
	assert.Equal(t, "big", plan.Entries[0].ID, "its id reaches the agent, so it can read it in parts")
}

func TestPlanner_NothingFitsMeansNothingIsMarkedRepresented(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	plan := pl.Plan(items("m", 3), 50) // not even one pointer line fits
	assert.Empty(t, plan.Entries)
	assert.Equal(t, 3, plan.Omitted)
	again := pl.Plan(items("m", 3), 10000)
	for _, e := range again.Entries {
		assert.True(t, e.Full, "ids that were never represented are still new")
	}
}

// C4, static backlog: asks that can never be confirmed, every result lost. Each id
// must still be represented within a bound, because the tail rotates. The bound is
// stated with the throughput the sizes allow: a body that fits goes out in full and
// costs its full size, a pointer line costs a compact one.
func TestPlanner_StaticBacklogEveryIdIsRepresentedWithinTheBound(t *testing.T) {
	t.Parallel()
	const (
		n      = 500
		budget = 4000
	)
	cases := []struct {
		name     string
		fullSize int
	}{
		{"bodies that fit: 9 full rows per result once a tail exists", testFull},
		{"bodies too big for the budget: pointer lines only", 90000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pl := NewPlanner()
			in := items("ask", n)
			for i := range in {
				in[i].FullSize = tc.fullSize
			}
			byID := index(in)

			perCallNew := (budget - tailReserveRows*testCompact) / testCompact // pointer lines
			if tc.fullSize <= budget {
				perCallNew = (budget - tailReserveRows*testCompact) / tc.fullSize // full rows
			}
			bound := (n+perCallNew-1)/perCallNew + 2

			seen := map[string]int{}
			for call := 1; call <= bound; call++ {
				plan := pl.Plan(in, budget) // the result is dropped on the floor each time
				require.LessOrEqual(t, planSize(plan, byID), budget)
				for _, e := range plan.Entries {
					if _, ok := seen[e.ID]; !ok {
						seen[e.ID] = call
					}
				}
			}
			assert.Len(t, seen, n, "every one of %d ids was represented within %d results", n, bound)
		})
	}
}

// A result that was lost: the ids it carried come round again.
func TestPlanner_AnIdWhoseResultWasLostComesRoundAgain(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	in := items("m", 30)
	const budget = 1500 // 15 pointer lines
	first := pl.Plan(in, budget)
	lost := map[string]bool{}
	for _, e := range first.Entries {
		lost[e.ID] = true
	}
	require.NotEmpty(t, lost)

	reappeared := false
	for call := 0; call < 10 && !reappeared; call++ {
		for _, e := range pl.Plan(in, budget).Entries {
			if lost[e.ID] {
				reappeared = true
			}
		}
	}
	assert.True(t, reappeared, "what was lost is offered again")
}

// C4, a new message with no older new one waiting: it is in the very next result,
// even with a tail that wants its rows.
func TestPlanner_ANewMessageWithNoNewBacklogIsInTheNextResult(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	in := items("old", 200)
	pl.Plan(in, 1<<20) // everything old has been represented once: a real tail, no new backlog
	urgent := Item{ID: "cancel-now", FullSize: testFull, CompactSize: testCompact}
	plan := pl.Plan(append(append([]Item{}, in...), urgent), 2000)
	var got *Entry
	for i := range plan.Entries {
		if plan.Entries[i].ID == "cancel-now" {
			got = &plan.Entries[i]
		}
	}
	require.NotNil(t, got, "the new message is in the very next result")
	assert.True(t, got.Full)
	assert.Equal(t, "cancel-now", plan.Entries[0].ID, "and it is first")
}

// C4, a new message behind an unrepresented backlog: NOT first (the planner has no
// notion of urgency), but bounded.
func TestPlanner_ANewMessageBehindANewBacklogComesAfterTheOlderOnesButWithinABound(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	backlog := items("b", 100)
	late := Item{ID: "late", FullSize: testFull, CompactSize: testCompact}
	all := append(append([]Item{}, backlog...), late)
	const budget = 2000 // 20 pointer lines

	first := pl.Plan(all, budget)
	for _, e := range first.Entries {
		assert.NotEqual(t, "late", e.ID, "it has older new messages ahead of it: the planner adds no priority")
	}
	perCallNew := (budget - tailReserveRows*testCompact) / testFull // full rows per result once a tail exists
	bound := (len(all)+perCallNew-1)/perCallNew + 2
	found := 0
	for call := 2; call <= bound; call++ {
		for _, e := range pl.Plan(all, budget).Entries {
			if e.ID == "late" {
				found = call
			}
		}
		if found > 0 {
			break
		}
	}
	assert.NotZero(t, found, "but it does arrive, within a bounded number of results")
}

// C4, no monopoly: a steady flood of new messages cannot starve the tail.
func TestPlanner_AFloodOfNewMessagesCannotStarveTheTail(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	tail := items("tail", 60)
	pl.Plan(tail, 100000) // everything represented once: this is the tail now
	const budget = 1000   // 10 pointer lines per result
	bound := (60+tailReserveRows-1)/tailReserveRows + 1

	served := map[string]bool{}
	for call := 0; call < bound; call++ {
		current := append(append([]Item{}, tail...), items(fmt.Sprintf("flood%02d-", call), 100)...)
		for _, e := range pl.Plan(current, budget).Entries {
			if len(e.ID) >= 4 && e.ID[:4] == "tail" {
				served[e.ID] = true
			}
		}
	}
	assert.Len(t, served, 60, "all of the original tail was served within %d results despite 100 new messages per result", bound)
}

func TestPlanner_ForgetsWhatLeftTheInbox(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	in := items("m", 50)
	pl.Plan(in, 100000)
	require.Equal(t, 50, pl.Tracked())
	pl.Plan(in[:10], 100000)
	assert.Equal(t, 10, pl.Tracked(), "archived messages are not remembered forever")
}

func TestPlanner_IsDeterministic(t *testing.T) {
	t.Parallel()
	in := items("m", 80)
	a, b := NewPlanner(), NewPlanner()
	for i := 0; i < 12; i++ {
		assert.Equal(t, a.Plan(in, 1800), b.Plan(in, 1800), "call %d", i)
	}
}

// Whatever the sizes, the plan never exceeds the budget, and never lists an id twice.
func TestPlanner_PropertyBudgetAndUniqueness(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(11))
	for round := 0; round < 200; round++ {
		pl := NewPlanner()
		n := rng.Intn(120)
		in := make([]Item, n)
		for i := range in {
			compact := 40 + rng.Intn(200)
			in[i] = Item{ID: fmt.Sprintf("p%03d", i), CompactSize: compact, FullSize: compact + rng.Intn(5000)}
		}
		byID := index(in)
		budget := 200 + rng.Intn(8000)
		for call := 0; call < 8; call++ {
			if rng.Intn(3) == 0 && len(in) > 0 { // some messages get archived between results
				in = in[:len(in)-1]
				byID = index(in)
			}
			plan := pl.Plan(in, budget)
			require.LessOrEqual(t, planSize(plan, byID), budget, "round %d call %d", round, call)
			seen := map[string]bool{}
			for _, e := range plan.Entries {
				require.False(t, seen[e.ID], "id %s listed twice", e.ID)
				seen[e.ID] = true
			}
			require.Equal(t, len(in)-len(plan.Entries), plan.Omitted)
		}
	}
}

func TestPlanner_SafeForConcurrentUse(t *testing.T) {
	pl := NewPlanner()
	in := items("m", 100)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				pl.Plan(in, 1500)
				pl.Tracked()
			}
		}()
	}
	wg.Wait()
}

// A small message must not overtake a big one: stopping at the first that does not
// fit even as a pointer keeps the oldest-first order. Skipping ahead would let
// small messages leave the big one behind for as long as small ones keep arriving.
func TestPlanner_SmallMessagesDoNotOvertakeOneThatDoesNotFit(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	in := []Item{
		{ID: "wide", FullSize: 900, CompactSize: 500},
		{ID: "tiny1", FullSize: 60, CompactSize: 50},
		{ID: "tiny2", FullSize: 60, CompactSize: 50},
	}
	plan := pl.Plan(in, 300)
	assert.Empty(t, plan.Entries, "the oldest does not fit even as a pointer, so nothing behind it goes first")
	assert.Equal(t, 3, plan.Omitted)
}

func TestPlanner_CountNewIsAReadAndSeesWhatAResultRepresented(t *testing.T) {
	t.Parallel()
	pl := NewPlanner()
	ids := []string{"a", "b", "c"}
	assert.Equal(t, 3, pl.CountNew(ids))
	assert.Equal(t, 3, pl.CountNew(ids), "asking does not represent anything")
	assert.Zero(t, pl.Tracked())

	pl.Plan([]Item{{ID: "a", FullSize: 10, CompactSize: 5}, {ID: "b", FullSize: 10, CompactSize: 5}}, 100)
	assert.Equal(t, 1, pl.CountNew(ids), "a and b were represented, c was not")
	assert.Zero(t, pl.CountNew([]string{"a"}))
	assert.Zero(t, pl.CountNew(nil))
}
