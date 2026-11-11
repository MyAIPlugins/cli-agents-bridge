#!/usr/bin/env bash
# F-154 measurement branch only (probe/f154-twin), never merged.
#
# A checker that has never been seen to fail is not a checker. This feeds judge.sh
# real-shaped logs, good and bad, and asserts what it says and its exit code. The
# workflow runs it first, so a judge that stopped discriminating cannot hand out
# a green.

set -u
here="$(cd "$(dirname "$0")" && pwd)"
judge="${here}/judge.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/judge-selftest.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

GRANT=TestDesktopGrant_ConcurrentOpenCloseNeverTearsTheFile
A=TestOpenNoFollow_DeniedWhileDeletePendingRidesOutToTheFileBeingGone
B=TestOpenNoFollow_DeniedForeverReportsTheRealErrorAfterTheBudget
C=TestOpenNoFollow_ASymlinkIsRefusedWithoutWaiting
D=TestOpenNoFollow_DeniedThenReplacedByALinkDuringTheWaitIsRefused
COST=TestF154Probe_PermanentDenialCost

failures=0
expect() { # <label> <want rc> <want text in output, or ''> <judge args...>
  label=$1 want_rc=$2 want_text=$3; shift 3
  out="$(bash "${judge}" "$@" 2>&1)"; rc=$?
  ok=yes
  [ "${rc}" -eq "${want_rc}" ] || ok="no (rc=${rc}, wanted ${want_rc})"
  if [ -n "${want_text}" ] && ! printf '%s\n' "${out}" | grep -q -- "${want_text}"; then ok="no (output lacks '${want_text}')"; fi
  if [ "${ok}" = yes ]; then echo "ok   ${label}"; else echo "FAIL ${label}: ${ok}"; printf '%s\n' "${out}" | head -5; failures=$((failures + 1)); fi
}

lines() { # <n> <line>: n copies
  i=0; while [ "${i}" -lt "$1" ]; do printf '%s\n' "$2"; i=$((i + 1)); done
}

# --- twin ---------------------------------------------------------------------
{ lines 100 "--- PASS: ${GRANT} (0.12s)"; } > "${work}/twin-clean.log"
expect "twin: 100 PASS" 0 "passed=100 failed=0 reader_errors=0" twin "${work}/twin-clean.log" 100

{ lines 92 "--- PASS: ${GRANT} (0.12s)"
  i=0; while [ "${i}" -lt 8 ]; do
    echo "    desktopgrant_test.go:187: torn read: desktop grant: read C:\\x\\desktop-grant.json: open C:\\x\\desktop-grant.json: Access is denied."
    echo "--- FAIL: ${GRANT} (0.20s)"; i=$((i + 1)); done; } > "${work}/twin-reader.log"
expect "twin: 8 reader denials counted" 0 "passed=92 failed=8 reader_errors=8 reader_denied=8 writer_errors=0" twin "${work}/twin-reader.log" 100

{ lines 90 "--- PASS: ${GRANT} (0.12s)"
  i=0; while [ "${i}" -lt 10 ]; do
    echo "    desktopgrant_test.go:166: close: rename C:\\x: Access is denied."
    echo "--- FAIL: ${GRANT} (0.20s)"; i=$((i + 1)); done; } > "${work}/twin-writer.log"
expect "twin: writer errors are not reader denials" 0 "reader_denied=0 writer_errors=10" twin "${work}/twin-writer.log" 100

{ lines 60 "--- PASS: ${GRANT} (0.12s)"; echo "panic: test timed out after 10m0s"; } > "${work}/twin-cut.log"
expect "twin: a truncated log is an error, not 'zero failures'" 1 "is not 100" twin "${work}/twin-cut.log" 100
: > "${work}/empty.log"
expect "twin: an empty log" 1 "missing or empty" twin "${work}/empty.log" 100
expect "twin: a missing log" 1 "missing or empty" twin "${work}/does-not-exist.log" 100

# --- t2 -----------------------------------------------------------------------
{ for n in "${A}" "${B}" "${C}" "${D}"; do echo "--- PASS: ${n} (0.01s)"; done; } > "${work}/t2-pass.log"
expect "t2 pass: four PASS" 0 "the four tests PASS" t2 "${work}/t2-pass.log" pass

{ for n in "${A}" "${B}" "${D}"; do echo "--- PASS: ${n} (0.01s)"; done; echo "--- SKIP: ${C} (0.00s)"; } > "${work}/t2-skip.log"
expect "t2 pass: a SKIP does not close the proof" 1 "${C} has no PASS" t2 "${work}/t2-skip.log" pass

{ for n in "${A}" "${B}" "${C}"; do echo "--- PASS: ${n} (0.01s)"; done; } > "${work}/t2-missing.log"
expect "t2 pass: a test that was not selected" 1 "${D} has no PASS" t2 "${work}/t2-missing.log" pass

{ for n in "${A}" "${B}" "${D}"; do echo "--- FAIL: ${n} (0.01s)"; done; echo "--- PASS: ${C} (0.00s)"; } > "${work}/t2-red.log"
expect "t2 red: the three that need the fix FAIL" 0 "premise held; symlink test: pass=1" t2 "${work}/t2-red.log" red

{ for n in "${A}" "${B}" "${D}"; do echo "--- FAIL: ${n} (0.01s)"; echo "    premise not reproduced: x"; done; } > "${work}/t2-premise.log"
expect "t2 red: red for a broken premise is refused" 1 "wrong reason" t2 "${work}/t2-premise.log" red

{ echo "--- FAIL: ${A} (0.01s)"; echo "--- FAIL: ${B} (0.01s)"; echo "--- PASS: ${D} (0.01s)"; } > "${work}/t2-green.log"
expect "t2 red: a test that is green without the fix" 1 "${D} is not FAIL" t2 "${work}/t2-green.log" red

# --- cost ---------------------------------------------------------------------
cost_log() { # <file> <K10 scan> <K10 scan_ms> <K33 read_ms> <K33 scan> <single_ms>
  {
    echo "=== RUN   ${COST}"
    echo "    zz.go:1: F154COST premise=denied single_read_ms=$6"
    echo "    zz.go:2: F154COST K=1 readmailbox_ms=160 entries=0 corrupt=1 foreign=0 read_err=<nil> scan_ms=161 scan=ok guard_busy_after_scan=false guard_free_after_ms=0 guard_never_freed=false"
    echo "    zz.go:3: F154COST K=10 readmailbox_ms=1560 entries=0 corrupt=10 foreign=0 read_err=<nil> scan_ms=$3 scan=$2 guard_busy_after_scan=false guard_free_after_ms=0 guard_never_freed=false"
    echo "    zz.go:4: F154COST K=33 readmailbox_ms=$4 entries=0 corrupt=33 foreign=0 read_err=<nil> scan_ms=5001 scan=$5 guard_busy_after_scan=true guard_free_after_ms=130 guard_never_freed=false"
    echo "    zz.go:5: F154COST K=67 readmailbox_ms=10400 entries=0 corrupt=67 foreign=0 read_err=<nil> scan_ms=5001 scan=timeout guard_busy_after_scan=true guard_free_after_ms=5400 guard_never_freed=false"
    echo "--- PASS: ${COST} (30.00s)"
  } > "$1"
}
cost_log "${work}/cost-good.log" ok 1590 5200 timeout 160
expect "cost: a good log" 0 "| 33 | 5200 | 5001 | timeout |" cost "${work}/cost-good.log"

cost_log "${work}/cost-k10.log" timeout 5000 5200 timeout 160
expect "cost: K=10 over the budget stops the line" 1 "75 ms" cost "${work}/cost-k10.log"

cost_log "${work}/cost-k10b.log" ok 5000 5200 timeout 160
expect "cost: K=10 at exactly the budget is not within it" 1 "do not fit" cost "${work}/cost-k10b.log"

cost_log "${work}/cost-k10refused.log" timeout 10 5200 timeout 160
expect "cost: K=10 refused at once (a guard answer, not a scan) is not 'within the budget'" 1 "do not fit" cost "${work}/cost-k10refused.log"

cost_log "${work}/cost-nowait.log" ok 1590 100 timeout 160
expect "cost: K=33 cheaper than its waits means the retry did not run" 1 "under the 5115 ms" cost "${work}/cost-nowait.log"

cost_log "${work}/cost-noscan.log" ok 1590 5200 ok 160
expect "cost: K=33 whose scan did not time out" 1 "not the timeout" cost "${work}/cost-noscan.log"

cost_log "${work}/cost-single.log" ok 1590 5200 timeout 10
expect "cost: a single denied read cheaper than the backoff" 1 "retry did not run" cost "${work}/cost-single.log"

sed 's/--- PASS: /--- SKIP: /' "${work}/cost-good.log" > "${work}/cost-skipped.log"
expect "cost: a skipped probe" 1 "has no PASS line" cost "${work}/cost-skipped.log"

grep -v 'K=67' "${work}/cost-good.log" > "${work}/cost-nok67.log"
expect "cost: a missing row" 1 "row for K=67 is missing" cost "${work}/cost-nok67.log"

sed 's/guard_never_freed=false/guard_never_freed=true/' "${work}/cost-good.log" > "${work}/cost-wedged.log"
expect "cost: a guard that never freed" 1 "never freed" cost "${work}/cost-wedged.log"

# --- fisher: the values were computed independently (exact hypergeometric, Python) ---
expect "fisher: 2 of 200 against 0 of 200 is weak" 0 "^0\.2494$" fisher 2 200 0 200
expect "fisher: 5 of 500 against 0 of 500" 0 "^0\.0309$" fisher 5 500 0 500
expect "fisher: 8 of 800 against 0 of 800" 0 "^0\.0038$" fisher 8 800 0 800
expect "fisher: 4 of 500 against 0 of 500 does not reach 0.05" 0 "^0\.0621$" fisher 4 500 0 500
expect "fisher: no failure on either side" 0 "^1\.0000$" fisher 0 500 0 500
expect "fisher: more failures than one group can hold" 0 "^0\.8000$" fisher 2 3 2 3
expect "fisher: every failure on the side that can hold them all" 0 "^1\.0000$" fisher 3 3 3 3
expect "fisher: groups of different size, first group worse" 0 "^0\.0267$" fisher 3 100 1 400
expect "fisher: groups of different size, first group better" 0 "^0\.9985$" fisher 1 400 3 100
# The same number under a decimal-comma locale must still print with a point (a host
# without that locale falls back to C, which is the same answer).
out="$(LC_ALL=it_IT.UTF-8 bash "${judge}" fisher 5 500 0 500 2>&1)"
if [ "${out}" = "0.0309" ]; then echo "ok   fisher: under a decimal-comma locale"; else echo "FAIL fisher: under a decimal-comma locale: got '${out}'"; failures=$((failures + 1)); fi
expect "fisher: more failures than runs" 1 "more failures than runs" fisher 3 2 0 5
expect "fisher: not a number" 1 "is not a number" fisher x 500 0 500
expect "fisher: an empty group" 1 "at least one run" fisher 0 0 0 500

if [ "${failures}" -ne 0 ]; then echo "::error title=judge selftest::${failures} case(s) failed"; exit 1; fi
echo "judge selftest: all cases behave"
