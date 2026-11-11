//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// F-154 COST PROBE. Measurement only: this file lives on probe/f154-twin and is
// never merged.
//
// Question: what does a PERMANENT denial (one that lasts the whole scan, unlike
// the millisecond window of the field defect) cost a scanner once openNoFollow
// asks again? The retry is bounded at 155 ms of requested backoff per file, and
// readMailbox walks every JSON file of a folder with no context and no cap, so the
// cost grows with the number of denied files. scanInboxWithin gives that walk a 5 s
// budget but does not stop the worker, so the scan guard stays busy until the walk
// ends.
//
// The fixture puts K files in the state of a delete that has not finished: delete
// disposition set, the deleting handle held open until the measurement ends. Lstat
// and open answer ACCESS_DENIED for as long as the handle is held, which is the
// error code a real ACL gives and the only thing the retry reacts to. The first
// version of this probe denied the read with `icacls /deny <sid>:(RD)`; on the
// windows-latest runner the denied file was read anyway (twin run 43021594948,
// "premise not reproduced"), so a denial that cannot be produced there is not a
// measurement. This one is the state the lab of probe 2 validated (run
// 43020768866: the name answers DELETE_PENDING while the handle is open).
//
// Two premises are ASSERTED, never assumed: a held file is refused with
// ACCESS_DENIED, and the directory scan still lists the K names (a probe that
// walked nothing would report a cost of nothing).
//
// Output is `F154COST key=value ...` lines the workflow parses. The test only
// fails on a broken premise; the thresholds are judged by the workflow.

const (
	costProbeEnv       = "F154_COST_PROBE"
	costProbeBudget    = 5 * time.Second // config.MCPScanBudgetSeconds default
	costProbeGuardPoll = 10 * time.Millisecond
	costProbeGuardMax  = 3000 // polls: 30 s
	costProbeMaxBytes  = 65536
)

// costProbeKs: 33 files are 5115 ms of waits at 155 ms each (over the 5 s budget),
// 67 are 5025 ms at 75 ms each (the same for the alternative budget).
var costProbeKs = []int{1, 10, 33, 67}

// fileDispositionInfo mirrors FILE_DISPOSITION_INFO, which is one BOOLEAN: one
// byte, and SetFileInformationByHandle checks the size.
type fileDispositionInfo struct{ DeleteFile byte }

// plantHeld writes n JSON files in dir and puts each in the state of a delete that
// has not finished. release closes every deleting handle, which completes the
// deletes; it is idempotent and also runs at cleanup, before the temp dir is
// removed.
func plantHeld(t *testing.T, dir string, n int) (paths []string, release func()) {
	t.Helper()
	paths = make([]string, 0, n)
	handles := make([]windows.Handle, 0, n)
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		for _, h := range handles {
			if err := windows.CloseHandle(h); err != nil {
				t.Errorf("CloseHandle of a deleting handle: %v", err)
			}
		}
	}
	t.Cleanup(release)

	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("msg-%012d.json", i))
		require.NoError(t, os.WriteFile(p, []byte("{}"), 0o600))
		name, err := windows.UTF16PtrFromString(p)
		require.NoError(t, err)
		h, err := windows.CreateFile(name, windows.DELETE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		require.NoError(t, err, "CreateFile(DELETE) on %s", p)
		handles = append(handles, h) // before the next call: a failure releases it at cleanup
		info := fileDispositionInfo{DeleteFile: 1}
		require.NoError(t, windows.SetFileInformationByHandle(h, windows.FileDispositionInfo,
			(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))), "SetFileInformationByHandle on %s", p)
		paths = append(paths, p)
	}
	return paths, release
}

// assertDeniedForGood is the first premise: a held file must be refused with
// ACCESS_DENIED, and the time that took is the cost of ONE permanent denial.
func assertDeniedForGood(t *testing.T) {
	t.Helper()
	paths, release := plantHeld(t, t.TempDir(), 1)
	defer release()
	start := time.Now()
	_, err := security.ReadOwnedFile(paths[0])
	took := time.Since(start)
	if err == nil {
		t.Fatalf("premise not reproduced: the file whose delete is in flight was read")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("premise not reproduced: the file whose delete is in flight answered %v, not ACCESS_DENIED", err)
	}
	t.Logf("F154COST premise=denied single_read_ms=%d", took.Milliseconds())
}

func scanVerdict(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, errInboxScanTimeout):
		return "timeout"
	default:
		return "other"
	}
}

func measureDenied(t *testing.T, k int) {
	t.Helper()
	dir := t.TempDir()
	_, release := plantHeld(t, dir, k)
	defer release()

	// The second premise: a name whose delete is in flight is still enumerated.
	listed, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, listed, k, "premise not reproduced: the directory scan must still list the %d names whose delete is in flight", k)

	start := time.Now()
	entries, corrupt, foreign, rerr := readMailbox(dir, costProbeMaxBytes)
	readMs := time.Since(start).Milliseconds()

	guard := &scanGuard{}
	ctx, cancel := context.WithTimeout(context.Background(), costProbeBudget)
	defer cancel()
	start = time.Now()
	_, scanErr := scanInboxWithin(ctx, guard, dir, costProbeMaxBytes)
	scanMs := time.Since(start).Milliseconds()
	busy := guard.busy.Load()

	freeMs := int64(0)
	start = time.Now()
	for i := 0; i < costProbeGuardMax && guard.busy.Load(); i++ {
		time.Sleep(costProbeGuardPoll)
	}
	if busy {
		freeMs = time.Since(start).Milliseconds()
	}
	stillBusy := guard.busy.Load()

	t.Logf("F154COST K=%d readmailbox_ms=%d entries=%d corrupt=%d foreign=%d read_err=%v scan_ms=%d scan=%s guard_busy_after_scan=%v guard_free_after_ms=%d guard_never_freed=%v",
		k, readMs, len(entries), len(corrupt), len(foreign), rerr, scanMs, scanVerdict(scanErr), busy, freeMs, stillBusy)
}

func TestF154Probe_PermanentDenialCost(t *testing.T) {
	if os.Getenv(costProbeEnv) != "1" {
		t.Skipf("measurement-only probe: set %s=1", costProbeEnv)
	}
	assertDeniedForGood(t)
	for _, k := range costProbeKs {
		measureDenied(t, k)
	}
}
