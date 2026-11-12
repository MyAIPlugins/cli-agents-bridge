//go:build windows

package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// PROBE 4 for F-170, criterion 4 of the BAR: the defect reproduced, and then gone. Not a
// regression test, never merged; it lives on probe/f170-windows-4, on top of the fix.
//
// WHAT IT RUNS. The shape of probe 3b's B1, which is the one that reproduced the denied close
// with ONE closer (58 events in about 18700 closes that found the file): a real opener
// (OpenDesktopGrant), a real reader (ReadDesktopGrant in a tight loop) and a closer PACED 1:1
// with the opener (it waits for a token the opener leaves after each completed open). Six
// independent environments, 90 s each side, one pass with the race detector and one without
// (the workflow runs the test twice).
//
// TWO SIDES, one question:
//
//	OFF  the closer is the BARE os.Remove, the call CloseDesktopGrant made before the fix. This is the
//	     PREMISE: it must be denied at least once per pass, or the run says "PREMISE NOT REPRODUCED"
//	     and fails: an ON side that shows no error is worth nothing if the defect was not there.
//	ON   the closer is mgr.CloseDesktopGrant, the fixed call, which asks again inside the bound of F-154.
//	     Every closer error is counted and any one fails the test. The probe adds NO retry of its own on
//	     either side (probe 3's diagnostic cycle retried up to 20000 times, and its "retry ended ok" said
//	     nothing about the first retry; here the only retry is the product's).
//
// WHAT IT RECORDS, for BOTH sides and EVERY actor: the outcome of each open, each read and each
// close, with the first few errors by time. The opener and reader errors are REPORTED (a line says
// so), not folded into a total, and they do not decide the verdict: they say whether the pressure
// was the one the closer was supposed to face.
//
// LIMITS, declared. (1) The internal retries of CloseDesktopGrant are not counted: that would need a
// public seam only for this, and none was added. What is visible is that no error comes out, and
// how long the calls took. (2) CloseDesktopGrant folds "the file was already gone" into nil, so on
// the ON side the closes that FOUND the file are not known; the OFF side knows them. The ON bound
// below uses an ESTIMATE from the OFF side of the same pass ([D]), and says so. (3) 0 errors in N is
// "not seen in N", with the rule-of-three bound printed, never "does not happen". (4) The budget
// (6 attempts, 155 ms requested) is an initial choice of F-154: a clean ON side says that, in this
// shape and on this runner, it was enough, nothing more.
//
// COMPILED FOR WINDOWS, NEVER RUN BY ITS AUTHOR.

const (
	p4EnvVar    = "F170_PROBE"
	p4For       = 90 * time.Second
	p4Envs      = 6
	p4MaxIter   = 5_000_000
	p4MaxEvents = 12
	// p4Budget is the backoff F-154 requests (5+10+20+40+80 ms), only to count the calls that took
	// longer than it. It is a reference for reading the table, not an assertion.
	p4Budget = 155 * time.Millisecond
)

type p4Tally struct {
	opener, reader, closer map[string]int
	closeSum, closeMax     time.Duration
	closeOver              int // close calls that took longer than the requested backoff
	events                 []string
}

func newP4Tally() *p4Tally {
	return &p4Tally{opener: map[string]int{}, reader: map[string]int{}, closer: map[string]int{}}
}

func (a *p4Tally) merge(o *p4Tally) {
	for _, p := range [][2]map[string]int{{a.opener, o.opener}, {a.reader, o.reader}, {a.closer, o.closer}} {
		for k, v := range p[1] {
			p[0][k] += v
		}
	}
	a.closeSum += o.closeSum
	a.closeOver += o.closeOver
	if o.closeMax > a.closeMax {
		a.closeMax = o.closeMax
	}
	for _, e := range o.events {
		if len(a.events) < p4MaxEvents {
			a.events = append(a.events, e)
		}
	}
}

func p4Total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// p4Errors is every outcome of m that is not a success: for the closer, "ok" is the only success
// (the bare remove also reports "notexist", which is a close that found nothing, not an error).
func p4Errors(m map[string]int, successes ...string) int {
	n := 0
	for k, v := range m {
		ok := false
		for _, s := range successes {
			if k == s {
				ok = true
			}
		}
		if !ok {
			n += v
		}
	}
	return n
}

func p4Class(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, fs.ErrNotExist):
		return "notexist"
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "DENIED"
	default:
		s := err.Error()
		if len(s) > 140 {
			s = s[:140]
		}
		return "other: " + s
	}
}

type p4Env struct {
	mgr     *Manager
	sid     string
	stop    atomic.Bool
	started time.Time
}

func (e *p4Env) event(ta *p4Tally, who string, err error) {
	if len(ta.events) < p4MaxEvents {
		ta.events = append(ta.events, fmt.Sprintf("at %s: %s: %v", time.Since(e.started).Round(time.Millisecond), who, err))
	}
}

// p4Run runs one side: p4Envs environments for p4For, each with its opener, its reader and its
// paced closer, where the closer calls closeOnce.
func p4Run(t *testing.T, closeOnce func(e *p4Env) error) (*p4Tally, time.Duration) {
	t.Helper()
	total := newP4Tally()
	var mu sync.Mutex
	var wg sync.WaitGroup
	started := time.Now()

	envs := make([]*p4Env, p4Envs)
	for i := range envs {
		mgr, sid, _ := newGrantVal(t)
		envs[i] = &p4Env{mgr: mgr, sid: sid, started: started}
	}
	spawn := func(body func(ta *p4Tally)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ta := newP4Tally()
			body(ta)
			mu.Lock()
			total.merge(ta)
			mu.Unlock()
		}()
	}
	for _, e := range envs {
		tokens := make(chan struct{}, 1)
		spawn(func(ta *p4Tally) { // the opener: the real OpenDesktopGrant, then a token
			for i := 0; i < p4MaxIter && !e.stop.Load(); i++ {
				_, err := e.mgr.OpenDesktopGrant(e.sid, time.Hour)
				ta.opener[p4Class(err)]++
				if err != nil {
					e.event(ta, "open", err)
				}
				select {
				case tokens <- struct{}{}:
				default:
					ta.opener["token dropped (the closer was behind)"]++
				}
			}
		})
		spawn(func(ta *p4Tally) { // the closer: one close per token
			for i := 0; i < p4MaxIter && !e.stop.Load(); i++ {
				select {
				case <-tokens:
					start := time.Now()
					err := closeOnce(e)
					took := time.Since(start)
					ta.closer[p4Class(err)]++
					ta.closeSum += took
					if took > ta.closeMax {
						ta.closeMax = took
					}
					if took > p4Budget {
						ta.closeOver++
					}
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						e.event(ta, "close", err)
					}
				case <-time.After(20 * time.Millisecond):
				}
			}
		})
		spawn(func(ta *p4Tally) { // the reader: the real ReadDesktopGrant in a tight loop
			for i := 0; i < p4MaxIter && !e.stop.Load(); i++ {
				_, found, err := e.mgr.ReadDesktopGrant(e.sid)
				switch {
				case err != nil:
					ta.reader[p4Class(err)]++
					e.event(ta, "read", err)
				case found:
					ta.reader["ok, grant found"]++
				default:
					ta.reader["ok, no grant"]++
				}
			}
		})
	}
	timer := time.AfterFunc(p4For, func() {
		for _, e := range envs {
			e.stop.Store(true)
		}
	})
	wg.Wait()
	timer.Stop()
	return total, time.Since(started)
}

func p4Counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

func p4Side(name string, ta *p4Tally, took time.Duration) string {
	var b strings.Builder
	closes := p4Total(ta.closer)
	fmt.Fprintf(&b, "[%s] %s of stress\n", name, took.Round(time.Second))
	fmt.Fprintf(&b, "    closer outcomes: %s\n", p4Counts(ta.closer))
	if closes > 0 {
		fmt.Fprintf(&b, "    close calls: %d, mean %s, max %s, longer than the requested backoff (%s): %d\n",
			closes, (ta.closeSum / time.Duration(closes)).Round(time.Microsecond), ta.closeMax.Round(time.Microsecond), p4Budget, ta.closeOver)
	}
	fmt.Fprintf(&b, "    opener outcomes: %s\n", p4Counts(ta.opener))
	fmt.Fprintf(&b, "    reader outcomes: %s\n", p4Counts(ta.reader))
	if side := p4Errors(ta.opener, "ok", "token dropped (the closer was behind)") + p4Errors(ta.reader, "ok, grant found", "ok, no grant"); side > 0 {
		fmt.Fprintf(&b, "    SIDE ERRORS: %d opener or reader errors on this side; the pressure on the closer may not be the intended one\n", side)
	}
	for _, ev := range ta.events {
		fmt.Fprintf(&b, "    event %s\n", ev)
	}
	return b.String()
}

func TestProbe4FixOnOff(t *testing.T) {
	if os.Getenv(p4EnvVar) != "1" {
		t.Skip("measurement only: set " + p4EnvVar + "=1 (the f170-probe4 workflow does)")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "PROBE 4 fix off/on (GOMAXPROCS=%d, NumCPU=%d, %d environments, %s per side)\n",
		runtime.GOMAXPROCS(0), runtime.NumCPU(), p4Envs, p4For)

	off, offTook := p4Run(t, func(e *p4Env) error { return os.Remove(e.mgr.desktopGrantPath(e.sid)) })
	out.WriteString(p4Side("OFF  closer = the bare os.Remove, CloseDesktopGrant before the fix", off, offTook))
	offDenied := off.closer["DENIED"]
	offFound := off.closer["ok"] + offDenied
	offCloses := p4Total(off.closer)
	if offFound > 0 {
		fmt.Fprintf(&out, "    CLOSES THAT FOUND THE FILE: %d (removed %d, denied %d), already gone %d\n", offFound, off.closer["ok"], offDenied, off.closer["notexist"])
	}
	if offDenied > 0 {
		fmt.Fprintf(&out, "    denied per close that found the file: %d in %d (1 in %d)\n", offDenied, offFound, offFound/offDenied)
	}

	on, onTook := p4Run(t, func(e *p4Env) error { return e.mgr.CloseDesktopGrant(e.sid) })
	out.WriteString(p4Side("ON   closer = mgr.CloseDesktopGrant, the fixed call", on, onTook))
	onCloses := p4Total(on.closer)
	onErrors := p4Errors(on.closer, "ok")
	fmt.Fprintf(&out, "    CLOSER ERRORS: %d in %d closes (every error counted; \"ok\" is removed-or-already-gone, not told apart)\n", onErrors, onCloses)
	if offCloses > 0 && offFound > 0 {
		est := float64(onCloses) * float64(offFound) / float64(offCloses)
		fmt.Fprintf(&out, "    estimated closes that found the file [D, from the OFF side of this pass]: about %.0f; "+
			"if the error rate were the OFF rate, the expected errors would be about %.1f\n", est, est*float64(offDenied)/float64(offFound))
		if onErrors == 0 && est > 0 {
			fmt.Fprintf(&out, "    0 errors: not seen in about %.0f closes that found the file (rule of three: the 95%% upper bound on the rate is about %.2g)\n", est, 3/est)
		}
	}

	switch {
	case offDenied == 0:
		out.WriteString("VERDICT: none. PREMISE NOT REPRODUCED: the bare os.Remove was never denied on the OFF side of this pass, so a clean ON side proves nothing.\n")
		t.Errorf("PREMISE NOT REPRODUCED: OFF side had 0 denied closes in %d closes (%d found the file)\n%s", offCloses, offFound, out.String())
		return
	case onErrors > 0:
		out.WriteString("VERDICT: the defect was reproduced on the OFF side and the ON side STILL RETURNED CLOSER ERRORS.\n")
		t.Errorf("ON CLOSER ERRORS: %d in %d closes\n%s", onErrors, onCloses, out.String())
		return
	}
	fmt.Fprintf(&out, "VERDICT: the defect was reproduced on the OFF side (%d denied closes) and the ON side returned no closer error in %d closes.\n", offDenied, onCloses)
	t.Log("PROBE 4 result\n" + out.String())
}
