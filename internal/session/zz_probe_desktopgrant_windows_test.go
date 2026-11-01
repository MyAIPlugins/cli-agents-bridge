//go:build windows

package session

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PROBE for the "Access is denied" of ReadDesktopGrant on Windows, not a regression test,
// and not meant to stay in the tree.
//
// TestDesktopGrant_ConcurrentOpenCloseNeverTearsTheFile failed once on windows-latest
// with "CreateFile <path>: Access is denied", coming out of ReadDesktopGrant. That text
// can only be produced by os.Lstat (stat_windows.go), the first line of openNoFollow:
// not by the open with FILE_SHARE_DELETE, and not by the rename. What is NOT known is
// which state of the file makes Windows answer "denied" there. This runs the same
// race, with the product's own read and each of the system calls it is made of read by
// a reader of its own, and when a call answers "denied" it keeps asking the same call
// to see HOW LONG the answer lasts and WHAT it becomes:
//
//	lasts milliseconds, then "not exist"   a name held by a delete that has not finished
//	                                       (a handle somebody else keeps open)
//	lasts microseconds, then "ok"          a window inside the replace itself
//	only the Lstat/GetFileAttributesEx     ... and not the plain open: the stat is the gap
//	readers deny, the share-delete open
//	does not
//
// It also says whether the denial fell INSIDE a call of the opener or of the closer, or
// how long after one had ended. It fails (so that CI prints the table) only if it saw a
// denial; otherwise it passes without a word.
//
// COMPILED FOR WINDOWS, NEVER RUN BY ITS AUTHOR: there is no Windows machine on the
// author's side. A first run whose table looks wrong is a bug in the probe before it is
// a finding.

type probeAPI struct {
	name string
	call func() error
}

type probeEvent struct {
	variant, api             string
	at, stayed               time.Duration
	then                     string
	inOpen, inClose          bool
	sinceOpenEnd, sinceClose time.Duration
}

func probeClass(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, os.ErrNotExist):
		return "notexist"
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "DENIED"
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
		return "sharing"
	default:
		return "other: " + err.Error()
	}
}

func TestProbeDesktopGrantDenied(t *testing.T) {
	mgr, sid, _ := newGrantVal(t)
	path := mgr.desktopGrantPath(sid)
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	const rounds, iterations = 4, 150

	createShareAll := func() error {
		h, err := windows.CreateFile(p, windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			_ = windows.CloseHandle(h)
		}
		return err
	}
	createNoAccess := func() error { // what os.Lstat falls back to
		h, err := windows.CreateFile(p, 0, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			_ = windows.CloseHandle(h)
		}
		return err
	}
	apis := []probeAPI{
		{"GetFileAttributesEx", func() error {
			var fa syscall.Win32FileAttributeData
			return syscall.GetFileAttributesEx(p, syscall.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&fa)))
		}},
		{"os.Lstat", func() error { _, err := os.Lstat(path); return err }},
		{"CreateFile(share-all)", createShareAll},
		{"CreateFile(no access, no share)", createNoAccess},
		{"ReadDesktopGrant", func() error { _, _, err := mgr.ReadDesktopGrant(sid); return err }},
	}

	type work func(stop *atomic.Bool, openSpan, closeSpan *[2]atomic.Int64)
	type variant struct {
		name string
		work []work
	}
	span := func(s *[2]atomic.Int64, f func()) {
		s[0].Store(time.Now().UnixNano())
		f()
		s[1].Store(time.Now().UnixNano())
	}
	opener := func(stop *atomic.Bool, openSpan, _ *[2]atomic.Int64) {
		for i := 0; i < iterations && !stop.Load(); i++ {
			span(openSpan, func() { _, _ = mgr.OpenDesktopGrant(sid, time.Hour) })
		}
	}
	closer := func(stop *atomic.Bool, _, closeSpan *[2]atomic.Int64) {
		for i := 0; i < iterations && !stop.Load(); i++ {
			span(closeSpan, func() { _ = mgr.CloseDesktopGrant(sid) })
		}
	}
	writeRemove := func(stop *atomic.Bool, _, closeSpan *[2]atomic.Int64) { // a delete with no rename in it
		for i := 0; i < iterations && !stop.Load(); i++ {
			_ = os.WriteFile(path, []byte("{}"), 0o600)
			span(closeSpan, func() { _ = os.Remove(path) })
		}
	}
	variants := []variant{
		{"open+close (the real test)", []work{opener, closer}},
		{"open only (replace, no delete)", []work{opener}},
		{"write+remove (delete, no rename)", []work{writeRemove}},
	}

	var mu sync.Mutex
	tally := map[string]map[string]int{} // "variant | api" -> class -> n
	var events []probeEvent
	denied := 0

	for _, v := range variants {
		for round := 0; round < rounds; round++ {
			_ = os.Remove(path)
			var stop atomic.Bool
			var openSpan, closeSpan [2]atomic.Int64
			started := time.Now()
			var writers, readers sync.WaitGroup
			for _, w := range v.work {
				writers.Add(1)
				go func() { defer writers.Done(); w(&stop, &openSpan, &closeSpan) }()
			}
			for _, a := range apis {
				readers.Add(1)
				go func() {
					defer readers.Done()
					key := v.name + " | " + a.name
					for !stop.Load() {
						class := probeClass(a.call())
						mu.Lock()
						if tally[key] == nil {
							tally[key] = map[string]int{}
						}
						tally[key][class]++
						mu.Unlock()
						if class != "DENIED" {
							continue
						}
						// The same call, again, until it stops saying DENIED (3s at most).
						at := time.Now()
						ev := probeEvent{variant: v.name, api: a.name, at: at.Sub(started), then: "still DENIED after 3s"}
						oS, oE, cS, cE := openSpan[0].Load(), openSpan[1].Load(), closeSpan[0].Load(), closeSpan[1].Load()
						ev.inOpen, ev.inClose = oS > oE, cS > cE
						ev.sinceOpenEnd = time.Duration(at.UnixNano() - oE)
						ev.sinceClose = time.Duration(at.UnixNano() - cE)
						for time.Since(at) < 3*time.Second {
							if c := probeClass(a.call()); c != "DENIED" {
								ev.then = c
								break
							}
							runtime.Gosched()
						}
						ev.stayed = time.Since(at)
						mu.Lock()
						denied++
						if len(events) < 24 {
							events = append(events, ev)
						}
						mu.Unlock()
					}
				}()
			}
			writers.Wait()
			stop.Store(true)
			readers.Wait()
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "PROBE desktop-grant denied: %d rounds of %d iterations per variant; %d denials\n", rounds, iterations, denied)
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		classes := make([]string, 0, len(tally[k]))
		for c := range tally[k] {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		parts := make([]string, 0, len(classes))
		for _, c := range classes {
			parts = append(parts, fmt.Sprintf("%s=%d", c, tally[k][c]))
		}
		fmt.Fprintf(&b, "  %-70s %s\n", k, strings.Join(parts, " "))
	}
	for _, e := range events {
		fmt.Fprintf(&b, "  DENIED %-32s in %-34s at %-9s stayed %-9s then %s | in open call=%v in close call=%v | since open ended %s, since close ended %s\n",
			e.api, e.variant, e.at.Round(time.Millisecond), e.stayed.Round(time.Microsecond), e.then,
			e.inOpen, e.inClose, e.sinceOpenEnd.Round(time.Microsecond), e.sinceClose.Round(time.Microsecond))
	}
	if denied > 0 {
		t.Error(b.String())
		return
	}
	t.Log(b.String())
}
