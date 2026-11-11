//go:build windows

package session

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// PROBE 3b for F-170. Not a regression test, never merged. It reuses the helpers and the
// recording of probe 3 (zz_probe3_f170_windows_test.go).
//
// WHY. Probe 3 (run 43117498447) showed both mechanisms exist and are deterministic in the lab
// (R1, R2), and that V2 (two closers) and V3 (raw replace against a remove) deny often. It did
// NOT reproduce the failure of the CI test, which has ONE closer: V1 saw 0 denials in 1.08
// million closes. But V1's closer ran about 100 closes per open, so almost every close found
// the file already gone and could not be denied by anything: the denominator was closes, and it
// should be closes that FIND THE FILE. The real test is 150 opens against 150 closes, and its
// rate (1 failure in 800 runs) is about 1 in 120000 opens. V1 had about 8700 opens: expected
// 0.07 events, observed 0. Undersized, not contradictory.
//
// WHAT THIS PROBE CHANGES. The closer is PACED 1:1 with the opener: it waits for an open to
// complete (a token the opener leaves after each OpenDesktopGrant) and then does the same
// os.Remove CloseDesktopGrant does. So every close finds a file, and the next open, which
// replaces that very file, lands while the close is still running. The outcome of the remove is
// recorded raw (ok = it removed the file, notexist = the file was gone) because
// CloseDesktopGrant folds both into nil, and "closes that found the file" is the denominator.
// The opener and the reader are the real ones.
//
// TWO VARIANTS, one question: does the reader matter?
//
//	B1 paced, with the real reader looping ReadDesktopGrant (the test's shape)
//	B2 paced, NO reader
//
// Candidate mechanisms for a denied close with ONE closer, all [D]:
//
//	H-b  the next open's POSIX replace lands between DeleteFileW's open and its disposition.
//	     Predicts: every denial overlaps a replace; NTSTATUS right after = SUCCESS (the name
//	     holds the new file); the retry ends ok (and has deleted the NEW file). It does not
//	     need a reader, so B2 shows it too.
//	H-r  the previous close of the same closer left a pending delete alive because a reader
//	     held the file (the VAL's candidate). Predicts: denials only in B1, never in B2;
//	     overlapping a reader; NTSTATUS right after = DELETE_PENDING or NOT_FOUND; and the
//	     opener's replace is denied too (R3 says it would be, for ~320 ms).
//	H-c  neither: a denial that overlaps no replace, no reader, no other delete.
//
// Predictions on the counts, so that the output can contradict them: B1 and B2 both show
// denials if H-b is the mechanism, only B1 if H-r is, and a rate per close-that-found-the-file
// well above the CI's 1 in 120000 per open, because pacing makes every close find a file.
// A zero is "not seen in N" with the rule-of-three bound printed.
//
// It always fails, so that CI prints the tables. COMPILED FOR WINDOWS, NEVER RUN BY ITS
// AUTHOR.

const (
	p3bFor  = 90 * time.Second
	p3bEnvs = 6

	// B3 is the test's own shape, round after round: 150 opens against 150 closes and a reader,
	// a new session each round, as many rounds as fit in the time. The CI saw 1 failure in 800
	// such rounds, so 12 environments for 150 s (about 1800 rounds if the disk keeps up) is where
	// the probe can expect an event or two per pass instead of none.
	p3bRoundFor    = 150 * time.Second
	p3bRoundEnvs   = 12
	p3bRoundWrites = 150
)

func p3bPacedActors(withReader bool) func(t *testing.T, e *p3Env) []p3Actor {
	return func(t *testing.T, e *p3Env) []p3Actor {
		mgr, sid, _ := newGrantVal(t)
		e.path = mgr.desktopGrantPath(sid)
		tokens := make(chan struct{}, 1)
		acts := []p3Actor{
			// The opener: the real OpenDesktopGrant, then a token. If the closer is behind the token
			// is dropped and counted, so the pacing stays honest instead of piling up.
			func(e *p3Env, ta *p3Tally) {
				p3Loop(e, func(int) {
					ta.writer["open: "+p3Class(e.span(&e.repSeq, func() error { _, err := mgr.OpenDesktopGrant(sid, time.Hour); return err }))]++
					select {
					case tokens <- struct{}{}:
					default:
						ta.writer["token dropped (the closer was behind)"]++
					}
				})
			},
			// The closer: one close per token, the same os.Remove CloseDesktopGrant calls.
			func(e *p3Env, ta *p3Tally) {
				for i := 0; i < p3MaxIter && !e.stop.Load(); i++ {
					select {
					case <-tokens:
						e.del(0, ta, func() error { return os.Remove(e.path) })
					case <-time.After(20 * time.Millisecond):
					}
				}
			},
		}
		if withReader {
			acts = append(acts, func(e *p3Env, ta *p3Tally) {
				p3Loop(e, func(int) {
					ta.reader[p3Class(e.span(&e.readSeq, func() error { _, _, err := mgr.ReadDesktopGrant(sid); return err }))]++
				})
			})
		}
		return acts
	}
}

// p3bRoundActor is B3: the shape of TestDesktopGrant_ConcurrentOpenCloseNeverTearsTheFile, with
// the outcome of the closer recorded instead of reported through t.Errorf. One goroutine per
// side, 150 iterations each, the closer NOT paced (it runs ahead of the opener, as in the test),
// the reader looping until both writers are done; then a new session and another round. The
// closer does the os.Remove CloseDesktopGrant does, raw, so that "ok" and "notexist" stay apart.
func p3bRoundActor(t *testing.T) p3Actor {
	return func(e *p3Env, ta *p3Tally) {
		for round := 0; round < p3MaxIter && !e.stop.Load(); round++ {
			mgr, sid, _ := newGrantVal(t)
			e.path = mgr.desktopGrantPath(sid)
			to, tc, tr := newP3Tally(), newP3Tally(), newP3Tally()
			var wg sync.WaitGroup
			stop := make(chan struct{})
			wg.Add(2)
			go func() { // the opener
				defer wg.Done()
				for i := 0; i < p3bRoundWrites; i++ {
					to.writer["open: "+p3Class(e.span(&e.repSeq, func() error { _, err := mgr.OpenDesktopGrant(sid, time.Hour); return err }))]++
				}
			}()
			go func() { // the closer
				defer wg.Done()
				for i := 0; i < p3bRoundWrites; i++ {
					e.del(0, tc, func() error { return os.Remove(e.path) })
				}
			}()
			readerDone := make(chan struct{})
			go func() { // the reader
				defer close(readerDone)
				for {
					select {
					case <-stop:
						return
					default:
					}
					tr.reader[p3Class(e.span(&e.readSeq, func() error { _, _, err := mgr.ReadDesktopGrant(sid); return err }))]++
				}
			}()
			wg.Wait()
			close(stop)
			<-readerDone
			ta.merge(to)
			ta.merge(tc)
			ta.merge(tr)
			ta.writer["rounds completed"]++
		}
	}
}

func p3bVariants() []p3Variant {
	return []p3Variant{
		{"B1 paced 1:1, real opener + real reader (the test's shape)", p3bFor, p3bEnvs, p3bPacedActors(true)},
		{"B2 paced 1:1, real opener, NO reader", p3bFor, p3bEnvs, p3bPacedActors(false)},
		{"B3 the test's own shape, round after round, closer NOT paced", p3bRoundFor, p3bRoundEnvs, func(t *testing.T, _ *p3Env) []p3Actor {
			return []p3Actor{p3bRoundActor(t)}
		}},
	}
}

// p3bExtra adds the two denominators the probe exists for: closes that found the file, and opens.
func p3bExtra(ta *p3Tally) string {
	found := ta.deleter["ok"] + ta.denied
	opens := ta.writer["open: ok"]
	var b strings.Builder
	fmt.Fprintf(&b, "    CLOSES THAT FOUND THE FILE: %d (removed %d, denied %d); opens %d; closes whose file was already gone %d\n",
		found, ta.deleter["ok"], ta.denied, opens, ta.deleter["notexist"])
	switch {
	case ta.denied > 0 && found > 0:
		fmt.Fprintf(&b, "    denied per close that found the file: %d in %d (1 in %d); per open: 1 in %d\n", ta.denied, found, found/ta.denied, max(opens, 1)/ta.denied)
	case found > 0:
		fmt.Fprintf(&b, "    denied per close that found the file: 0 in %d (rule of three: the 95%% upper bound is %.2g)\n", found, 3/float64(found))
	}
	return b.String()
}

func TestProbe3bPaced(t *testing.T) {
	if os.Getenv(p3EnvVar) != "1" {
		t.Skip("measurement only: set " + p3EnvVar + "=1 (the f170-probe3b workflow does)")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "STRESS 3b (GOMAXPROCS=%d, NumCPU=%d)\n", runtime.GOMAXPROCS(0), runtime.NumCPU())
	for _, v := range p3bVariants() {
		ta, took := p3RunVariant(t, v)
		out.WriteString(p3Report(v, ta, took))
		out.WriteString(p3bExtra(ta))
	}
	// ALWAYS an error: a package that passes prints nothing. This branch is never merged.
	t.Error("PROBE 3b delete denied\n" + out.String())
}
