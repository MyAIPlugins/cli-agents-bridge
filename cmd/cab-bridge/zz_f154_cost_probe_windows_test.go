//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// F-154 COST PROBE. Measurement only: this file lives on probe/f154-twin and is
// never merged.
//
// Question: what does a PERMANENT denial (an ACL, not a name caught in a delete) cost
// a scanner once openNoFollow asks again? The retry is bounded at 155 ms of
// requested backoff per file, and readMailbox walks every JSON file of a folder
// with no context and no cap, so the cost grows with the number of denied files.
// scanInboxWithin gives that walk a 5 s budget but does not stop the worker, so
// the scan guard stays busy until the walk ends.
//
// The fixture denies the read of K files with `icacls /deny <sid>:(RD)`: Lstat
// still works, an open for GENERIC_READ is refused for good. Whether that holds on
// the runner (an elevated token) is the PREMISE, and it is asserted: if the
// denied file reads, the probe fails instead of reporting a cost of nothing.
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

func currentUserSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err, "the token's user SID")
	return "*" + user.User.Sid.String()
}

// plantDenied writes n JSON files in dir and denies the read of each to sid. The
// ACL is reset at cleanup so the temp dir can be removed.
func plantDenied(t *testing.T, dir, sid string, n int) []string {
	t.Helper()
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("msg-%012d.json", i))
		require.NoError(t, os.WriteFile(p, []byte("{}"), 0o600))
		out, err := exec.Command("icacls", p, "/deny", sid+":(RD)").CombinedOutput()
		require.NoError(t, err, "icacls /deny: %s", out)
		paths = append(paths, p)
		t.Cleanup(func() {
			if out, rerr := exec.Command("icacls", p, "/reset").CombinedOutput(); rerr != nil {
				t.Logf("icacls /reset %s: %v: %s", p, rerr, out)
			}
		})
	}
	return paths
}

// assertDeniedForGood is the premise: a denied file must be refused with
// ACCESS_DENIED, and the time that took is the cost of ONE permanent denial.
func assertDeniedForGood(t *testing.T, sid string) {
	t.Helper()
	p := plantDenied(t, t.TempDir(), sid, 1)[0]
	start := time.Now()
	_, err := security.ReadOwnedFile(p)
	took := time.Since(start)
	if err == nil {
		t.Fatalf("premise not reproduced: the file with a deny ACE was read")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("premise not reproduced: the denied file answered %v, not ACCESS_DENIED", err)
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

func measureDenied(t *testing.T, sid string, k int) {
	t.Helper()
	dir := t.TempDir()
	plantDenied(t, dir, sid, k)

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
	sid := currentUserSID(t)
	assertDeniedForGood(t, sid)
	for _, k := range costProbeKs {
		measureDenied(t, sid, k)
	}
}
