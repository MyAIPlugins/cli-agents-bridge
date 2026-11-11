//go:build windows

package session

import (
	"encoding/binary"
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

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// PROBE 3 for F-170. Not a regression test, never merged.
//
// WHAT IS KNOWN. [E] F-154 probe 2: a name with a deleting handle open (the disposition set,
// the handle not yet closed) answers STATUS_DELETE_PENDING, which Win32 reports as
// ACCESS_DENIED, to an open or a stat; it answers NOT_FOUND once the handle closes. [E] The
// twin measurement of F-154 (run 43024786909): the original test, with the fix for READS,
// failed 1 time in 800 on the WRITER: CloseDesktopGrant's os.Remove returned "Access is
// denied" (desktopgrant_test.go:170). Probe 2 threw away the outcome of the side that
// deleted, so it could not say anything about that. THIS PROBE RECORDS THE OUTCOME OF EVERY
// SIDE: the deleter, the replacer and the reader, in every row and every variant.
//
// WHAT IS NOT KNOWN. Two working hypotheses, both [D], for the denied os.Remove:
//
//	H-a  another delete of the same name is in flight when this os.Remove arrives. In the
//	     original test there is no second deleter; in production there is if two commands
//	     close the same grant together.
//	H-b  DeleteFileW is two steps, an open with DELETE and then the disposition on the
//	     handle [D: believed, not traced]. If the POSIX replace of the other writer lands in
//	     between, the disposition is set on a file that is already unlinked, and the answer
//	     is DELETE_PENDING. It would explain a denied delete with ONE deleter.
//	H-c  neither: a denied delete that overlaps no delete and no replace (a scanner, a
//	     filter, the machine). The serial control (V0) has no concurrency at all: a denial
//	     there is H-c and nothing else.
//
// PREDICTIONS, declared before the run so that the output can contradict them:
//
//	R1  os.Remove against a delete in flight answers ACCESS_DENIED while the deleting handle
//	    is open, in all three disposition modes, and the name is free the moment it closes.
//	    (If it answered ok or notexist, H-a would be dead: DeleteFileW would not be refused
//	    by a pending delete.)
//	R2  I DO NOT KNOW what the disposition does on a handle whose file the replace already
//	    unlinked. Three outcomes are possible and each means something different: an error
//	    (H-b has a mechanism), success with the NEW file intact (harmless), success with the
//	    new file gone (a delete that lands on the wrong file).
//	V0  zero denials. A denial here refutes "it is the concurrency".
//	V1..V5 denials of the deleter are rare (1 in ~120000 closes in the twin run): zero in a
//	    variant is "not seen in N calls" with the rule-of-three bound printed, never
//	    "cannot happen".
//
// A denial is classified by what it overlapped, read BEFORE and AFTER the call as a
// per-operation sequence number (odd or changed = the call overlapped it): a replace, the
// other deleter, both, or neither. Right after each denial the probe samples the NTSTATUS of
// an open of the name, then retries the same os.Remove and says how the retry ended, which is
// what a bounded retry would see.
//
// It always fails, so that CI prints the tables whether or not a denial came. COMPILED FOR
// WINDOWS, NEVER RUN BY ITS AUTHOR: a table that looks wrong is a bug in the probe before it
// is a finding.

const (
	p3EnvVar = "F170_PROBE"

	// Time bounds, per variant. A loop here stops at the deadline or at p3MaxIterations.
	p3SerialFor    = 30 * time.Second
	p3GrantFor     = 45 * time.Second
	p3RawFor       = 120 * time.Second
	p3RecreateFor  = 60 * time.Second
	p3RawTwoDelFor = 60 * time.Second
	p3MaxIter      = 20_000_000
	p3RawPairs     = 4
	p3SmallPairs   = 2
	p3MaxRetryFor  = 3 * time.Second
	p3MaxRetryTry  = 20_000
	p3MaxEvents    = 12

	p3NtDeletePending    = 0xC0000056
	p3NtAccessDenied     = 0xC0000022
	p3NtNameNotFound     = 0xC0000034
	p3NtPathNotFound     = 0xC000003A
	p3NtSharingViolation = 0xC0000043

	p3DispDelete = 0x1
	p3DispPosix  = 0x2

	p3ShareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
)

type p3DispLegacy struct{ DeleteFile byte }
type p3DispEx struct{ Flags uint32 }

type p3Mode struct {
	name      string
	ex, posix bool
}

var p3Modes = []p3Mode{
	{"legacy FileDispositionInfo", false, false},
	{"FileDispositionInfoEx DELETE", true, false},
	{"FileDispositionInfoEx DELETE|POSIX", true, true},
}

// ---- small Win32 helpers ----

func p3NtName(code uint32) string {
	switch code {
	case 0:
		return "SUCCESS"
	case p3NtDeletePending:
		return "DELETE_PENDING"
	case p3NtAccessDenied:
		return "ACCESS_DENIED"
	case p3NtNameNotFound, p3NtPathNotFound:
		return "NOT_FOUND"
	case p3NtSharingViolation:
		return "SHARING_VIOLATION"
	default:
		return fmt.Sprintf("0x%08X", code)
	}
}

// p3NtOpen opens path by name with NtCreateFile and returns the NTSTATUS.
func p3NtOpen(path string) uint32 {
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
	err = windows.NtCreateFile(&h, windows.FILE_GENERIC_READ, oa, &iosb, &alloc, 0, p3ShareAll,
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

// p3Class names the outcome of an operation. "other" carries the text, so nothing is hidden
// in a bucket.
func p3Class(err error) string {
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

func p3IsDenied(err error) bool { return errors.Is(err, windows.ERROR_ACCESS_DENIED) }

func p3Stat(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "bad path"
	}
	var fa syscall.Win32FileAttributeData
	return p3Class(syscall.GetFileAttributesEx(p, syscall.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&fa))))
}

func p3CreateShareAll(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "bad path"
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, p3ShareAll, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err == nil {
		if cerr := windows.CloseHandle(h); cerr != nil {
			return "ok, but CloseHandle: " + cerr.Error()
		}
	}
	return p3Class(err)
}

// p3Name is what a name answers, three ways at once: stat | open | NTSTATUS.
func p3Name(path string) string {
	return fmt.Sprintf("%-9s | %-9s | %s", p3Stat(path), p3CreateShareAll(path), p3NtName(p3NtOpen(path)))
}

func p3OpenRead(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.GENERIC_READ, p3ShareAll, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

// p3OpenForDelete is the FIRST step of a delete: a handle with DELETE access, share all, the
// disposition not set yet.
func p3OpenForDelete(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.DELETE, p3ShareAll, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

// p3SetDisposition is the SECOND step: the delete disposition on the handle.
func p3SetDisposition(h windows.Handle, m p3Mode) error {
	if !m.ex {
		info := p3DispLegacy{DeleteFile: 1}
		return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	}
	info := p3DispEx{Flags: p3DispDelete}
	if m.posix {
		info.Flags |= p3DispPosix
	}
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfoEx, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
}

// p3Deleter is both steps, handle kept open: a delete IN FLIGHT.
func p3Deleter(path string, m p3Mode) (windows.Handle, error) {
	h, err := p3OpenForDelete(path)
	if err != nil {
		return 0, err
	}
	if err := p3SetDisposition(h, m); err != nil {
		_ = windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

// p3RenameReplace is the replace the bridge does (FileRenameInfoEx, REPLACE_IF_EXISTS |
// POSIX_SEMANTICS, internal/transport/fs/rename_windows.go) without the fsync and without the
// retry, so that a loop of it is fast and a refusal is seen instead of absorbed.
type p3RenameHdr struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
}

func p3RenameReplace(oldpath, newpath string) (err error) {
	op, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(op, windows.DELETE|windows.SYNCHRONIZE, p3ShareAll, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := windows.CloseHandle(h); cerr != nil && err == nil {
			err = cerr
		}
	}()
	name, err := windows.UTF16FromString(newpath)
	if err != nil {
		return err
	}
	hdr := unsafe.Offsetof(p3RenameHdr{}.FileNameLength) + unsafe.Sizeof(p3RenameHdr{}.FileNameLength)
	buf := make([]byte, int(hdr)+len(name)*2)
	binary.LittleEndian.PutUint32(buf[unsafe.Offsetof(p3RenameHdr{}.Flags):], windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS)
	binary.LittleEndian.PutUint32(buf[unsafe.Offsetof(p3RenameHdr{}.FileNameLength):], uint32((len(name)-1)*2))
	for i, c := range name {
		binary.LittleEndian.PutUint16(buf[int(hdr)+i*2:], c)
	}
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, &buf[0], uint32(len(buf)))
}

// ---- the lab: deterministic rows, the outcome of every side recorded ----

func p3Lab(t *testing.T) (text string, premiseFailures []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "LAB (name answers: GetFileAttributesEx | CreateFile share-all | NtCreateFile NTSTATUS)\n")
	plant := func(content string) string {
		p := filepath.Join(t.TempDir(), "grant.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("lab plant: %v", err)
		}
		return p
	}
	closeH := func(h windows.Handle, what string) {
		if err := windows.CloseHandle(h); err != nil {
			fmt.Fprintf(&b, "    (CloseHandle of %s: %v)\n", what, err)
		}
	}
	content := func(p string) string {
		data, err := os.ReadFile(p)
		if err != nil {
			return "read " + p3Class(err)
		}
		return fmt.Sprintf("content %q", string(data))
	}

	fmt.Fprintf(&b, "  R1 (H-a): os.Remove while ANOTHER delete is in flight (a handle with the disposition set, not closed)\n")
	for _, m := range p3Modes {
		p := plant("OLD")
		d, err := p3Deleter(p, m)
		if err != nil {
			fmt.Fprintf(&b, "    (%s: could not build the state: %v)\n", m.name, err)
			premiseFailures = append(premiseFailures, "R1 "+m.name+": "+err.Error())
			continue
		}
		fmt.Fprintf(&b, "    %-36s in flight, name answers          %s\n", m.name, p3Name(p))
		rmErr := os.Remove(p)
		pp, _ := windows.UTF16PtrFromString(p)
		rawErr := windows.DeleteFile(pp)
		fmt.Fprintf(&b, "    %-36s os.Remove returned               %s | raw DeleteFile %s\n", m.name, p3Class(rmErr), p3Class(rawErr))
		if p3NtOpen(p) != p3NtDeletePending {
			premiseFailures = append(premiseFailures, "R1 "+m.name+": the name does not answer DELETE_PENDING while the deleter is open")
		}
		closeH(d, "the in-flight deleter")
		fmt.Fprintf(&b, "    %-36s after the deleter closed         name %s\n", m.name, p3Name(p))
		fmt.Fprintf(&b, "    %-36s then os.Remove returned          %s\n", m.name, p3Class(os.Remove(p)))
	}
	{
		// The persistent state: legacy disposition, a share-delete reader still open.
		p := plant("OLD")
		rd, rerr := p3OpenRead(p)
		d, derr := p3Deleter(p, p3Modes[0])
		if rerr != nil || derr != nil {
			fmt.Fprintf(&b, "    (legacy + reader: reader=%v deleter=%v)\n", rerr, derr)
			premiseFailures = append(premiseFailures, fmt.Sprintf("R1 legacy+reader: reader=%v deleter=%v", rerr, derr))
		} else {
			closeH(d, "the legacy deleter")
			fmt.Fprintf(&b, "    legacy, deleter CLOSED, reader open  name answers                   %s\n", p3Name(p))
			fmt.Fprintf(&b, "    legacy, deleter CLOSED, reader open  os.Remove returned             %s\n", p3Class(os.Remove(p)))
			closeH(rd, "the reader")
			fmt.Fprintf(&b, "    legacy, reader closed too            os.Remove returned             %s\n", p3Class(os.Remove(p)))
		}
	}

	fmt.Fprintf(&b, "  R2 (H-b): DeleteFileW by hand: open with DELETE, THEN the POSIX replace lands, THEN the disposition\n")
	for _, m := range p3Modes {
		p := plant("OLD")
		h, err := p3OpenForDelete(p)
		if err != nil {
			fmt.Fprintf(&b, "    (%s: open with DELETE: %v)\n", m.name, err)
			premiseFailures = append(premiseFailures, "R2 "+m.name+": open with DELETE: "+err.Error())
			continue
		}
		repErr := transportfs.AtomicWriteBytes(p, []byte("NEW"), 0o600)
		fmt.Fprintf(&b, "    %-36s replace returned                 %s\n", m.name, p3Class(repErr))
		if repErr != nil {
			// The row exists to put the disposition AFTER a replace that happened; without one it
			// measures something else.
			premiseFailures = append(premiseFailures, "R2 "+m.name+": the replace did not succeed: "+repErr.Error())
		}
		setErr := p3SetDisposition(h, m)
		fmt.Fprintf(&b, "    %-36s disposition on the old handle     %s\n", m.name, p3Class(setErr))
		fmt.Fprintf(&b, "    %-36s name, old handle open             %s | %s\n", m.name, p3Name(p), content(p))
		closeH(h, "the old handle")
		fmt.Fprintf(&b, "    %-36s name, old handle closed           %s | %s\n", m.name, p3Name(p), content(p))
		_ = os.Remove(p)
	}

	fmt.Fprintf(&b, "  R3 (the opener's side): the replace while a delete is in flight\n")
	for _, m := range p3Modes {
		p := plant("OLD")
		d, err := p3Deleter(p, m)
		if err != nil {
			fmt.Fprintf(&b, "    (%s: could not build the state: %v)\n", m.name, err)
			premiseFailures = append(premiseFailures, "R3 "+m.name+": "+err.Error())
			continue
		}
		start := time.Now()
		repErr := transportfs.AtomicWriteBytes(p, []byte("NEW"), 0o600)
		fmt.Fprintf(&b, "    %-36s replace returned                 %s (took %s)\n", m.name, p3Class(repErr), time.Since(start).Round(time.Millisecond))
		closeH(d, "the in-flight deleter")
		fmt.Fprintf(&b, "    %-36s name after the deleter closed     %s | %s\n", m.name, p3Name(p), content(p))
		_ = os.Remove(p)
	}
	return b.String(), premiseFailures
}

// ---- the race: every side's outcome recorded ----

type p3Tally struct {
	calls                                  int
	deleter, writer, reader, rightAfter    map[string]int
	retryEnds                              map[string]int
	denied, overRep, overDel, overBoth, no int
	overRead                               int // denials that overlapped a reader call, whatever else they overlapped
	maxStay                                time.Duration
	delSum, delMax                         time.Duration
	events                                 []string
}

func newP3Tally() *p3Tally {
	return &p3Tally{deleter: map[string]int{}, writer: map[string]int{}, reader: map[string]int{}, rightAfter: map[string]int{}, retryEnds: map[string]int{}}
}

func (a *p3Tally) merge(o *p3Tally) {
	a.calls += o.calls
	for _, p := range [][2]map[string]int{{a.deleter, o.deleter}, {a.writer, o.writer}, {a.reader, o.reader}, {a.rightAfter, o.rightAfter}, {a.retryEnds, o.retryEnds}} {
		for k, v := range p[1] {
			p[0][k] += v
		}
	}
	a.denied += o.denied
	a.overRep += o.overRep
	a.overDel += o.overDel
	a.overBoth += o.overBoth
	a.no += o.no
	a.overRead += o.overRead
	a.delSum += o.delSum
	if o.maxStay > a.maxStay {
		a.maxStay = o.maxStay
	}
	if o.delMax > a.delMax {
		a.delMax = o.delMax
	}
	for _, e := range o.events {
		if len(a.events) < p3MaxEvents {
			a.events = append(a.events, e)
		}
	}
}

// p3Env is one set of actors on one path. Sequence numbers are odd while an operation runs.
type p3Env struct {
	path    string
	stop    atomic.Bool
	repSeq  atomic.Int64
	delSeq  [2]atomic.Int64
	readSeq atomic.Int64
	// prev is how each deleter's PREVIOUS call ended. Only deleter idx touches prev[idx].
	prev    [2]p3Prev
	started time.Time
}

type p3Prev struct {
	end   time.Time
	class string
}

func (e *p3Env) span(seq *atomic.Int64, f func() error) error {
	seq.Add(1)
	err := f()
	seq.Add(1)
	return err
}

// del is one delete by deleter idx. It records the outcome ALWAYS, and for a denial what it
// overlapped, the NTSTATUS right after, and how a retry of the same call ended.
func (e *p3Env) del(idx int, ta *p3Tally, f func() error) {
	other := &e.delSeq[1-idx]
	r0, d0, q0 := e.repSeq.Load(), other.Load(), e.readSeq.Load()
	prev := e.prev[idx]
	start := time.Now()
	err := e.span(&e.delSeq[idx], f)
	took := time.Since(start)
	r1, d1, q1 := e.repSeq.Load(), other.Load(), e.readSeq.Load()
	e.prev[idx] = p3Prev{end: time.Now(), class: p3Class(err)}

	ta.calls++
	ta.deleter[p3Class(err)]++
	ta.delSum += took
	if took > ta.delMax {
		ta.delMax = took
	}
	if !p3IsDenied(err) {
		return
	}
	ta.denied++
	overRep := r0&1 == 1 || r0 != r1
	overDel := d0&1 == 1 || d0 != d1
	overRead := q0&1 == 1 || q0 != q1
	if overRead {
		ta.overRead++
	}
	switch {
	case overRep && overDel:
		ta.overBoth++
	case overRep:
		ta.overRep++
	case overDel:
		ta.overDel++
	default:
		ta.no++
	}
	ta.rightAfter[p3NtName(p3NtOpen(e.path))]++
	at := time.Now()
	then := fmt.Sprintf("still denied after %s or %d tries", p3MaxRetryFor, p3MaxRetryTry)
	for i := 0; i < p3MaxRetryTry && time.Since(at) < p3MaxRetryFor; i++ {
		if rerr := f(); !p3IsDenied(rerr) {
			then = p3Class(rerr)
			break
		}
		runtime.Gosched()
	}
	ta.retryEnds[then]++
	if stay := time.Since(at); stay > ta.maxStay {
		ta.maxStay = stay
	}
	if len(ta.events) < p3MaxEvents {
		gap := "none"
		if !prev.end.IsZero() {
			gap = fmt.Sprintf("%s ended %s before this call started", prev.class, start.Sub(prev.end).Round(time.Microsecond))
		}
		ta.events = append(ta.events, fmt.Sprintf("at %s: %v | over replace=%v over other delete=%v over reader=%v | this call took %s | previous call of this deleter: %s | retry ended %s after %s",
			at.Sub(e.started).Round(time.Millisecond), err, overRep, overDel, overRead, took.Round(time.Microsecond), gap, then, time.Since(at).Round(time.Microsecond)))
	}
}

// p3Actor is one goroutine's body; it fills its own tally and loops until e.stop or p3MaxIter.
type p3Actor func(e *p3Env, ta *p3Tally)

type p3Variant struct {
	name   string
	dur    time.Duration
	envs   int
	actors func(t *testing.T, e *p3Env) []p3Actor
}

func p3RunVariant(t *testing.T, v p3Variant) (*p3Tally, time.Duration) {
	total := newP3Tally()
	var mu sync.Mutex
	var wg sync.WaitGroup
	started := time.Now()
	envs := make([]*p3Env, v.envs)
	for i := range envs {
		envs[i] = &p3Env{started: started}
	}
	for _, e := range envs {
		acts := v.actors(t, e)
		for _, act := range acts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ta := newP3Tally()
				act(e, ta)
				mu.Lock()
				total.merge(ta)
				mu.Unlock()
			}()
		}
	}
	timer := time.AfterFunc(v.dur, func() {
		for _, e := range envs {
			e.stop.Store(true)
		}
	})
	wg.Wait()
	timer.Stop()
	return total, time.Since(started)
}

func p3Loop(e *p3Env, body func(i int)) {
	for i := 0; i < p3MaxIter && !e.stop.Load(); i++ {
		body(i)
	}
}

// ---- the variants ----

// Raw: a path and a temp, no Manager. The replacer writes the temp (no fsync) and replaces the
// target with the bridge's own rename; the deleter is the real os.Remove.
func p3RawReplacer(e *p3Env, ta *p3Tally) {
	tmp := e.path + ".tmp"
	p3Loop(e, func(int) {
		werr := os.WriteFile(tmp, []byte("NEW"), 0o600)
		ta.writer["write: "+p3Class(werr)]++
		if werr != nil {
			return
		}
		ta.writer["replace: "+p3Class(e.span(&e.repSeq, func() error { return p3RenameReplace(tmp, e.path) }))]++
	})
}

func p3RawRemover(idx int) p3Actor {
	return func(e *p3Env, ta *p3Tally) {
		p3Loop(e, func(int) { e.del(idx, ta, func() error { return os.Remove(e.path) }) })
	}
}

func p3NewPath(t *testing.T) string { return filepath.Join(t.TempDir(), "grant.json") }

// Manager: the real OpenDesktopGrant / CloseDesktopGrant / ReadDesktopGrant.
func p3GrantActors(closers int) func(t *testing.T, e *p3Env) []p3Actor {
	return func(t *testing.T, e *p3Env) []p3Actor {
		mgr, sid, _ := newGrantVal(t)
		e.path = mgr.desktopGrantPath(sid)
		acts := []p3Actor{
			func(e *p3Env, ta *p3Tally) {
				p3Loop(e, func(int) {
					ta.writer["open: "+p3Class(e.span(&e.repSeq, func() error { _, err := mgr.OpenDesktopGrant(sid, time.Hour); return err }))]++
				})
			},
			func(e *p3Env, ta *p3Tally) {
				p3Loop(e, func(int) {
					ta.reader[p3Class(e.span(&e.readSeq, func() error { _, _, err := mgr.ReadDesktopGrant(sid); return err }))]++
				})
			},
		}
		for i := 0; i < closers; i++ {
			idx := i
			acts = append(acts, func(e *p3Env, ta *p3Tally) {
				p3Loop(e, func(int) { e.del(idx, ta, func() error { return mgr.CloseDesktopGrant(sid) }) })
			})
		}
		return acts
	}
}

func p3Variants() []p3Variant {
	return []p3Variant{
		{"V0 control: serial write+remove, NO concurrency", p3SerialFor, 1, func(t *testing.T, e *p3Env) []p3Actor {
			e.path = p3NewPath(t)
			return []p3Actor{func(e *p3Env, ta *p3Tally) {
				p3Loop(e, func(int) {
					ta.writer["write: "+p3Class(os.WriteFile(e.path, []byte("{}"), 0o600))]++
					e.del(0, ta, func() error { return os.Remove(e.path) })
				})
			}}
		}},
		{"V1 real code: opener + 1 closer + reader (the test's shape)", p3GrantFor, 1, p3GrantActors(1)},
		{"V2 real code: opener + 2 closers + reader (H-a in production)", p3GrantFor, 1, p3GrantActors(2)},
		{"V3 raw: POSIX replace vs 1 os.Remove (H-b), 4 pairs", p3RawFor, p3RawPairs, func(t *testing.T, e *p3Env) []p3Actor {
			e.path = p3NewPath(t)
			return []p3Actor{p3RawReplacer, p3RawRemover(0)}
		}},
		{"V4 raw: recreate vs 2 os.Remove (H-a), 2 pairs", p3RecreateFor, p3SmallPairs, func(t *testing.T, e *p3Env) []p3Actor {
			e.path = p3NewPath(t)
			return []p3Actor{func(e *p3Env, ta *p3Tally) {
				p3Loop(e, func(int) {
					ta.writer["create: "+p3Class(e.span(&e.repSeq, func() error { return os.WriteFile(e.path, []byte("{}"), 0o600) }))]++
				})
			}, p3RawRemover(0), p3RawRemover(1)}
		}},
		{"V5 raw: POSIX replace vs 2 os.Remove (H-a + H-b), 2 pairs", p3RawTwoDelFor, p3SmallPairs, func(t *testing.T, e *p3Env) []p3Actor {
			e.path = p3NewPath(t)
			return []p3Actor{p3RawReplacer, p3RawRemover(0), p3RawRemover(1)}
		}},
	}
}

func p3Report(v p3Variant, ta *p3Tally, took time.Duration) string {
	var b strings.Builder
	rate := "none"
	if ta.calls > 0 && ta.denied == 0 {
		rate = fmt.Sprintf("0 in %d calls (rule of three: the 95%% upper bound is %.2g per call)", ta.calls, 3/float64(ta.calls))
	} else if ta.calls > 0 {
		rate = fmt.Sprintf("%d in %d calls (1 in %d)", ta.denied, ta.calls, ta.calls/ta.denied)
	}
	fmt.Fprintf(&b, "  %s\n    ran %s, %d envs\n", v.name, took.Round(time.Second), v.envs)
	fmt.Fprintf(&b, "    deleter outcomes  %s\n", p3Counts(ta.deleter))
	fmt.Fprintf(&b, "    writer outcomes   %s\n", p3Counts(ta.writer))
	if len(ta.reader) > 0 {
		fmt.Fprintf(&b, "    reader outcomes   %s\n", p3Counts(ta.reader))
	}
	fmt.Fprintf(&b, "    DELETER DENIALS   %s\n", rate)
	if ta.denied > 0 {
		fmt.Fprintf(&b, "      overlap: over a replace only %d, over the other delete only %d, over both %d, over NEITHER %d (over a reader, with or without the others: %d)\n", ta.overRep, ta.overDel, ta.overBoth, ta.no, ta.overRead)
		fmt.Fprintf(&b, "      NTSTATUS right after: %s\n", p3Counts(ta.rightAfter))
		fmt.Fprintf(&b, "      the same call retried ended: %s (longest %s)\n", p3Counts(ta.retryEnds), ta.maxStay.Round(time.Microsecond))
		for _, ev := range ta.events {
			fmt.Fprintf(&b, "      %s\n", ev)
		}
	}
	if ta.calls > 0 {
		fmt.Fprintf(&b, "    delete calls: mean %s, max %s\n", (ta.delSum / time.Duration(ta.calls)).Round(time.Microsecond), ta.delMax.Round(time.Microsecond))
	}
	return b.String()
}

func p3Counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, "  ")
}

func TestProbe3DeleteDenied(t *testing.T) {
	if os.Getenv(p3EnvVar) != "1" {
		t.Skip("measurement only: set " + p3EnvVar + "=1 (the f170-probe3 workflow does)")
	}
	lab, premise := p3Lab(t)
	var stress strings.Builder
	fmt.Fprintf(&stress, "STRESS (GOMAXPROCS=%d, NumCPU=%d)\n", runtime.GOMAXPROCS(0), runtime.NumCPU())
	for _, v := range p3Variants() {
		ta, took := p3RunVariant(t, v)
		stress.WriteString(p3Report(v, ta, took))
	}
	for _, f := range premise {
		t.Errorf("PREMISE NOT REPRODUCED: %s", f)
	}
	// ALWAYS an error: a package that passes prints nothing, so a run with no denial would be
	// indistinguishable from a probe that did not run. This branch is never merged.
	t.Error("PROBE 3 delete denied\n" + lab + "\n" + stress.String())
}
