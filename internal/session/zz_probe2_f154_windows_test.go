//go:build windows

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// PROBE 2 for F-154. Not a regression test, never merged. It replaces the first
// probe (PR #25), whose result was: the denial is transient, hits a stat as much as
// an open, ends in "not found" or "ok", and needs a DELETE (the variant with a
// rename and no delete produced none; the variant with a delete and no rename
// produced the most).
//
// What the first run of the fix branch added (twin run 43017039092, Windows Server
// 2025): after os.Remove of a file a share-delete reader holds, the name is free AT
// ONCE. So a name that stays "delete-pending" after the delete returns is not what
// os.Remove does there, and a fixture built on it was refuted.
//
// THE CAUSE IS NOT ESTABLISHED. Three working hypotheses, all [D], and what each
// predicts in the output of this probe:
//
//	H1  a DELETE IN FLIGHT with legacy semantics: a handle with the delete
//	    disposition set and not yet closed. An open or a stat of the name by
//	    someone else is answered STATUS_DELETE_PENDING, which Win32 reports as
//	    ACCESS_DENIED; once the call returns the name is free.
//	      NtCreateFile reader: DELETE_PENDING. Every denial overlaps a delete call.
//	      Lab: the "legacy FileDispositionInfo" row answers DELETE_PENDING while the
//	      deleting handle is open.
//	H2  a momentary ACCESS_DENIED from the kernel while a POSIX delete unlinks the
//	    name. Tension with H1: if DeleteFile uses POSIX semantics here (the effect
//	    measured by run 43017039092 says so) the name goes away when the
//	    disposition is set, and a window that shows DELETE_PENDING needs a legacy
//	    fallback nobody has measured.
//	      NtCreateFile reader: ACCESS_DENIED (0xC0000022). Every denial overlaps a
//	      delete call. Lab: no still state produces it, "Ex DELETE|POSIX" answers
//	      NOT_FOUND at once.
//	H3  something outside the delete (a scanner or a filter on a file just written).
//	      Denials that overlap NO delete call, printed at the end; the "open only"
//	      variant would produce some, and the first probe saw none.
//
// What the rows decide:
//
//	P1  lab: which still state answers DELETE_PENDING, and whether closing the
//	    deleting handle frees the name. It is also the fixture the fix's Windows
//	    tests use, so it must hold on the runner
//	P2  race: the share of denials that overlap a delete call, and the list of the
//	    ones that overlap neither a delete nor the opener (they refute H1 and H2)
//	P3  race: the NTSTATUS of the NtCreateFile reader, which separates H1 from H2
//	P4  race: no denial under the variant with a rename and no delete
//
// The first probe stamped "was a delete in flight?" AFTER the denied call had
// returned, so a reader preempted in between landed "outside". This one reads a
// per-operation sequence number BEFORE and AFTER the call: odd or changed means the
// call overlapped a delete.
//
// It always fails, so that CI prints the tables whether or not it saw a denial.
// COMPILED FOR WINDOWS, NEVER RUN BY ITS AUTHOR: a table that looks wrong is a bug
// in the probe before it is a finding.

const (
	ntDeletePending    = 0xC0000056
	ntAccessDenied     = 0xC0000022
	ntNameNotFound     = 0xC0000034
	ntPathNotFound     = 0xC000003A
	ntSharingViolation = 0xC0000043
	ntCannotDelete     = 0xC0000121

	dispositionFlagDelete = 0x1
	dispositionFlagPosix  = 0x2

	probeShareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
)

type fileDispositionInfoLegacy struct{ DeleteFile byte }
type fileDispositionInfoEx struct{ Flags uint32 }

func ntName(code uint32) string {
	switch code {
	case 0:
		return "SUCCESS"
	case ntDeletePending:
		return "DELETE_PENDING"
	case ntAccessDenied:
		return "ACCESS_DENIED"
	case ntNameNotFound, ntPathNotFound:
		return "NOT_FOUND"
	case ntSharingViolation:
		return "SHARING_VIOLATION"
	case ntCannotDelete:
		return "CANNOT_DELETE"
	default:
		return fmt.Sprintf("0x%08X", code)
	}
}

// ntOpen opens path by name with NtCreateFile and returns the NTSTATUS.
func ntOpen(path string) uint32 {
	name, err := windows.NewNTUnicodeString(`\??\` + path)
	if err != nil {
		return 0xFFFFFFFF
	}
	oa := &windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var (
		h     windows.Handle
		iosb  windows.IO_STATUS_BLOCK
		alloc int64
	)
	err = windows.NtCreateFile(&h, windows.FILE_GENERIC_READ, oa, &iosb, &alloc, 0, probeShareAll,
		windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err == nil {
		_ = windows.CloseHandle(h)
		return 0
	}
	var st windows.NTStatus
	if errors.As(err, &st) {
		return uint32(st)
	}
	return 0xFFFFFFFF
}

func winClass(err error) string {
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

func statByName(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "bad path"
	}
	var fa syscall.Win32FileAttributeData
	return winClass(syscall.GetFileAttributesEx(p, syscall.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&fa))))
}

func createShareAll(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "bad path"
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, probeShareAll, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err == nil {
		_ = windows.CloseHandle(h)
	}
	return winClass(err)
}

// ---- the lab: deterministic states ----

func openRead(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.GENERIC_READ, probeShareAll, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

// openDeleter opens with DELETE access and sets the delete disposition, legacy or
// Ex(+POSIX), and keeps the handle.
func openDeleter(path string, ex, posix bool) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.DELETE, probeShareAll, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, err
	}
	if !ex {
		info := fileDispositionInfoLegacy{DeleteFile: 1}
		err = windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	} else {
		info := fileDispositionInfoEx{Flags: dispositionFlagDelete}
		if posix {
			info.Flags |= dispositionFlagPosix
		}
		err = windows.SetFileInformationByHandle(h, windows.FileDispositionInfoEx, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	}
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

func runLab(t *testing.T) string {
	var b strings.Builder
	fmt.Fprintf(&b, "LAB: what a name answers in each deterministic state (GetFileAttributesEx | CreateFile share-all | NtCreateFile NTSTATUS)\n")
	row := func(state, step, path string) {
		fmt.Fprintf(&b, "  %-58s %-34s %-9s | %-9s | %s\n", state, step, statByName(path), createShareAll(path), ntName(ntOpen(path)))
	}
	plant := func(name string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatalf("lab plant: %v", err)
		}
		return p
	}
	closeH := func(h windows.Handle) {
		if err := windows.CloseHandle(h); err != nil {
			fmt.Fprintf(&b, "  (CloseHandle: %v)\n", err)
		}
	}

	p := plant("l0.json")
	row("L0 the file, nobody else", "as planted", p)

	p = plant("l1.json")
	r, rerr := openRead(p)
	if rerr != nil {
		fmt.Fprintf(&b, "  (L1 reader failed: %v)\n", rerr)
	}
	rmErr := os.Remove(p)
	row("L1 share-delete reader open, then os.Remove", fmt.Sprintf("Remove returned: %s", winClass(rmErr)), p)
	if rerr == nil {
		closeH(r)
		row("L1 share-delete reader open, then os.Remove", "after the reader closed", p)
	}

	for _, c := range []struct {
		name      string
		ex, posix bool
	}{{"legacy FileDispositionInfo", false, false}, {"FileDispositionInfoEx DELETE", true, false}, {"FileDispositionInfoEx DELETE|POSIX", true, true}} {
		// L2: the deleting handle alone, kept open.
		p = plant("l2.json")
		d, err := openDeleter(p, c.ex, c.posix)
		if err != nil {
			fmt.Fprintf(&b, "  (L2 %s: %v)\n", c.name, err)
		} else {
			row("L2 deleter handle kept open, "+c.name, "delete in flight", p)
			closeH(d)
			row("L2 deleter handle kept open, "+c.name, "after the deleter closed", p)
		}
		// L3: a share-delete reader open as well, the deleter closes first.
		p = plant("l3.json")
		rd, rerr := openRead(p)
		d, derr := openDeleter(p, c.ex, c.posix)
		if rerr != nil || derr != nil {
			fmt.Fprintf(&b, "  (L3 %s: reader=%v deleter=%v)\n", c.name, rerr, derr)
			if rerr == nil {
				closeH(rd)
			}
			if derr == nil {
				closeH(d)
			}
			continue
		}
		row("L3 reader + deleter, "+c.name, "deleter open", p)
		closeH(d)
		row("L3 reader + deleter, "+c.name, "deleter closed, reader open", p)
		closeH(rd)
		row("L3 reader + deleter, "+c.name, "both closed", p)
	}

	// L4: a reader that does NOT share deletion, then os.Remove.
	p = plant("l4.json")
	f, err := os.Open(p)
	if err != nil {
		fmt.Fprintf(&b, "  (L4 os.Open: %v)\n", err)
	} else {
		rmErr = os.Remove(p)
		row("L4 reader without share-delete (os.Open), then os.Remove", fmt.Sprintf("Remove returned: %s", winClass(rmErr)), p)
		_ = f.Close()
	}

	// How long DeleteFile itself runs, quiet machine, for scale.
	var maxDur, sumDur time.Duration
	const n = 300
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		q := filepath.Join(dir, fmt.Sprintf("d%d.json", i))
		if err := os.WriteFile(q, []byte("{}"), 0o600); err != nil {
			t.Fatalf("lab plant: %v", err)
		}
		s := time.Now()
		_ = os.Remove(q)
		d := time.Since(s)
		sumDur += d
		if d > maxDur {
			maxDur = d
		}
	}
	fmt.Fprintf(&b, "  os.Remove, quiet, %d calls: mean %s, max %s\n", n, (sumDur / n).Round(time.Microsecond), maxDur.Round(time.Microsecond))
	return b.String()
}

// ---- the race ----

type overlapTally struct{ denied, overDelete, overOpenOnly, neither int }

type neitherEvent struct {
	variant, api, class string
	at, stayed          time.Duration
	then                string
}

func runStress(t *testing.T) string {
	mgr, sid, _ := newGrantVal(t)
	path := mgr.desktopGrantPath(sid)
	const rounds, iterations = 4, 150

	type reader struct {
		name string
		call func() string
	}
	readers := []reader{
		{"GetFileAttributesEx", func() string { return statByName(path) }},
		{"CreateFile(share-all)", func() string { return createShareAll(path) }},
		{"NtCreateFile", func() string { return ntName(ntOpen(path)) }},
	}
	isDenial := func(class string) bool {
		return class == "DENIED" || class == "DELETE_PENDING" || class == "ACCESS_DENIED"
	}

	type variant struct {
		name string
		work func(delSeq, openSeq *atomic.Int64, delDur *durStats, stop *atomic.Bool) []func()
	}
	span := func(seq *atomic.Int64, f func()) {
		seq.Add(1)
		f()
		seq.Add(1)
	}
	variants := []variant{
		{"write+remove (delete, no rename)", func(delSeq, _ *atomic.Int64, delDur *durStats, stop *atomic.Bool) []func() {
			return []func(){func() {
				for i := 0; i < iterations && !stop.Load(); i++ {
					_ = os.WriteFile(path, []byte("{}"), 0o600)
					span(delSeq, func() { s := time.Now(); _ = os.Remove(path); delDur.add(time.Since(s)) })
				}
			}}
		}},
		{"open+close (the real test)", func(delSeq, openSeq *atomic.Int64, delDur *durStats, stop *atomic.Bool) []func() {
			return []func(){
				func() {
					for i := 0; i < iterations && !stop.Load(); i++ {
						span(openSeq, func() { _, _ = mgr.OpenDesktopGrant(sid, time.Hour) })
					}
				},
				func() {
					for i := 0; i < iterations && !stop.Load(); i++ {
						span(delSeq, func() { s := time.Now(); _ = mgr.CloseDesktopGrant(sid); delDur.add(time.Since(s)) })
					}
				},
			}
		}},
		{"open only (replace, no delete)", func(_, openSeq *atomic.Int64, _ *durStats, stop *atomic.Bool) []func() {
			return []func(){func() {
				for i := 0; i < iterations && !stop.Load(); i++ {
					span(openSeq, func() { _, _ = mgr.OpenDesktopGrant(sid, time.Hour) })
				}
			}}
		}},
	}

	var mu sync.Mutex
	statuses := map[string]map[string]int{} // "variant | api" -> class -> n
	overlap := map[string]*overlapTally{}
	rightAfter := map[string]map[string]int{} // "variant | api" -> NTSTATUS of a sample taken right after the denial
	var neither []neitherEvent
	maxStay := map[string]time.Duration{}
	delDurs := map[string]*durStats{}

	for _, v := range variants {
		delDur := &durStats{}
		delDurs[v.name] = delDur
		for round := 0; round < rounds; round++ {
			_ = os.Remove(path)
			var stop atomic.Bool
			var delSeq, openSeq atomic.Int64
			started := time.Now()
			var writers, readersWG sync.WaitGroup
			for _, w := range v.work(&delSeq, &openSeq, delDur, &stop) {
				writers.Add(1)
				go func() { defer writers.Done(); w() }()
			}
			for _, rd := range readers {
				readersWG.Add(1)
				go func() {
					defer readersWG.Done()
					key := v.name + " | " + rd.name
					// Counted locally and merged at the end: a shared lock taken on every
					// call would serialize the readers and lower the very rate of denials
					// this probe is trying to observe.
					local := map[string]int{}
					defer func() {
						mu.Lock()
						defer mu.Unlock()
						if statuses[key] == nil {
							statuses[key] = map[string]int{}
						}
						for class, n := range local {
							statuses[key][class] += n
						}
					}()
					for !stop.Load() {
						d0, o0 := delSeq.Load(), openSeq.Load()
						class := rd.call()
						d1, o1 := delSeq.Load(), openSeq.Load()
						local[class]++
						if !isDenial(class) {
							continue
						}
						overDelete := d0&1 == 1 || d0 != d1
						overOpen := o0&1 == 1 || o0 != o1
						sample := ntName(ntOpen(path))
						at := time.Now()
						then := "still denied after 3s"
						for time.Since(at) < 3*time.Second {
							if c := rd.call(); !isDenial(c) {
								then = c
								break
							}
							runtime.Gosched()
						}
						stayed := time.Since(at)
						mu.Lock()
						ot := overlap[key]
						if ot == nil {
							ot = &overlapTally{}
							overlap[key] = ot
						}
						ot.denied++
						switch {
						case overDelete:
							ot.overDelete++
						case overOpen:
							ot.overOpenOnly++
						default:
							ot.neither++
							if len(neither) < 12 {
								neither = append(neither, neitherEvent{v.name, rd.name, class, at.Sub(started), stayed, then})
							}
						}
						if rightAfter[key] == nil {
							rightAfter[key] = map[string]int{}
						}
						rightAfter[key][sample]++
						if stayed > maxStay[v.name] {
							maxStay[v.name] = stayed
						}
						mu.Unlock()
					}
				}()
			}
			writers.Wait()
			stop.Store(true)
			readersWG.Wait()
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "STRESS: %d rounds of %d iterations per variant\n", rounds, iterations)
	fmt.Fprintf(&b, "  results per reader (class -> count):\n")
	for _, k := range sortedKeys(statuses) {
		fmt.Fprintf(&b, "    %-62s %s\n", k, countsLine(statuses[k]))
	}
	fmt.Fprintf(&b, "  P2 overlap with a delete call (denied / over a delete / over the opener only / over neither):\n")
	for _, k := range sortedKeys(overlap) {
		ot := overlap[k]
		fmt.Fprintf(&b, "    %-62s %d / %d / %d / %d\n", k, ot.denied, ot.overDelete, ot.overOpenOnly, ot.neither)
	}
	fmt.Fprintf(&b, "  P3 NTSTATUS sampled right after a denial (class -> count):\n")
	for _, k := range sortedKeys(rightAfter) {
		fmt.Fprintf(&b, "    %-62s %s\n", k, countsLine(rightAfter[k]))
	}
	fmt.Fprintf(&b, "  longest denial per variant, and the delete calls themselves (n, mean, max):\n")
	for _, v := range variants {
		n, mean, max := delDurs[v.name].summary()
		fmt.Fprintf(&b, "    %-40s denial max %-10s delete calls %d, mean %s, max %s\n", v.name, maxStay[v.name].Round(time.Microsecond), n, mean, max)
	}
	fmt.Fprintf(&b, "  denials that overlapped NEITHER a delete nor the opener (they refute H1 and H2):\n")
	if len(neither) == 0 {
		fmt.Fprintf(&b, "    none\n")
	}
	for _, e := range neither {
		fmt.Fprintf(&b, "    %-34s %-22s %-14s at %-9s stayed %-10s then %s\n", e.variant, e.api, e.class, e.at.Round(time.Millisecond), e.stayed.Round(time.Microsecond), e.then)
	}
	return b.String()
}

type durStats struct {
	mu       sync.Mutex
	n        int
	sum, max time.Duration
}

func (s *durStats) add(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.sum += d
	if d > s.max {
		s.max = d
	}
}

func (s *durStats) summary() (int, time.Duration, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == 0 {
		return 0, 0, 0
	}
	return s.n, (s.sum / time.Duration(s.n)).Round(time.Microsecond), s.max.Round(time.Microsecond)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func countsLine(m map[string]int) string {
	classes := sortedKeys(m)
	parts := make([]string, 0, len(classes))
	for _, c := range classes {
		parts = append(parts, fmt.Sprintf("%s=%d", c, m[c]))
	}
	return strings.Join(parts, " ")
}

func TestProbe2DeleteInFlight(t *testing.T) {
	lab := runLab(t)
	stress := runStress(t)
	// ALWAYS an error: a package that passes prints nothing, so a run with no
	// denial would be indistinguishable from a probe that did not run. This branch
	// is never merged, so a red package costs nothing.
	t.Error("PROBE 2 desktop-grant denied\n" + lab + "\n" + stress)
}
