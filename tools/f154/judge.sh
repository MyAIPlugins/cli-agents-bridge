#!/usr/bin/env bash
# F-154 measurement branch only (probe/f154-twin), never merged.
#
# judge.sh reads the LOG a test run left behind and decides, so the workflow does
# not have to trust an exit code. Absence is not a result: every subcommand fails
# when the line it needs is missing, and says which one.
#
#   judge.sh twin <log> <runs>     the original grant test, -count=<runs>: prints
#                                  counts; fails if PASS + FAIL is not <runs>
#   judge.sh t2   <log> pass|red   the four Windows tests of F-154 by NAME:
#                                  pass = all four PASS (SKIP or missing fails);
#                                  red  = the three that need the fix are FAIL,
#                                  and not for a broken premise
#   judge.sh cost <log>            the permanent-denial cost probe: PASS, the
#                                  premise, one row per K, and the thresholds
#
# Written for bash 3.2 (macOS) and Git Bash (Windows): no associative arrays.

set -u

GRANT_TEST=TestDesktopGrant_ConcurrentOpenCloseNeverTearsTheFile
T2_RIDES_OUT=TestOpenNoFollow_DeniedWhileDeletePendingRidesOutToTheFileBeingGone
T2_FOREVER=TestOpenNoFollow_DeniedForeverReportsTheRealErrorAfterTheBudget
T2_SYMLINK=TestOpenNoFollow_ASymlinkIsRefusedWithoutWaiting
T2_REPLACED=TestOpenNoFollow_DeniedThenReplacedByALinkDuringTheWaitIsRefused
COST_TEST=TestF154Probe_PermanentDenialCost

# Thresholds of the accepted plan, in milliseconds.
MCP_BUDGET_MS=5000   # config.MCPScanBudgetSeconds default
PER_FILE_MS=155      # 5+10+20+40+80
FILES_OVER_BUDGET=33 # 33 x 155 = 5115 ms of waits alone
FILES_WITHIN_BUDGET=10

fail() { echo "::error title=F-154 measurement::$*"; exit 1; }

# count_lines <extended regex> <file>: how many lines match, always a number.
count_lines() { grep -c -E "$1" "$2" || true; }

top_result() { # <PASS|FAIL|SKIP> <test name> <log>: lines `--- PASS: Name (0.00s)` at column 0
  count_lines "^--- $1: $2 " "$3"
}

cmd_twin() {
  log=$1 runs=$2
  [ -s "${log}" ] || fail "twin: the log ${log} is missing or empty"
  passed=$(top_result PASS "${GRANT_TEST}" "${log}")
  failed=$(top_result FAIL "${GRANT_TEST}" "${log}")
  if [ $((passed + failed)) -ne "${runs}" ]; then
    fail "twin: ${passed} PASS + ${failed} FAIL is not ${runs} (a build failure or a timeout is not 'zero failures')"
  fi
  reader=$(count_lines 'torn read:' "${log}")
  reader_denied=$(count_lines 'torn read:.*[Aa]ccess is denied' "${log}")
  writer=$(count_lines '\.go:[0-9]+: (open|close): ' "${log}")
  echo "passed=${passed} failed=${failed} reader_errors=${reader} reader_denied=${reader_denied} writer_errors=${writer}"
}

t2_must_pass() { # <name> <log>
  [ "$(top_result PASS "$1" "$2")" -eq 1 ] \
    || fail "t2: ${1} has no PASS line (a SKIP, a FAIL or a name that was not selected does not close the proof)"
}

t2_must_fail() {
  [ "$(top_result FAIL "$1" "$2")" -eq 1 ] \
    || fail "t2: ${1} is not FAIL without the fix, so it proves nothing about the fix"
}

cmd_t2() {
  log=$1 expect=$2
  [ -s "${log}" ] || fail "t2: the log ${log} is missing or empty"
  case "${expect}" in
    pass)
      for name in "${T2_RIDES_OUT}" "${T2_FOREVER}" "${T2_SYMLINK}" "${T2_REPLACED}"; do t2_must_pass "${name}" "${log}"; done
      echo "t2 with the fix: the four tests PASS"
      ;;
    red)
      for name in "${T2_RIDES_OUT}" "${T2_FOREVER}" "${T2_REPLACED}"; do t2_must_fail "${name}" "${log}"; done
      if grep -q 'premise not reproduced' "${log}"; then
        fail "t2: red for the wrong reason: the delete-pending premise was not reproduced"
      fi
      echo "t2 without the fix: the three tests that need it are FAIL, and the premise held; symlink test: pass=$(top_result PASS "${T2_SYMLINK}" "${log}") skip=$(top_result SKIP "${T2_SYMLINK}" "${log}") fail=$(top_result FAIL "${T2_SYMLINK}" "${log}")"
      ;;
    *) fail "t2: expectation must be pass or red, got '${expect}'" ;;
  esac
}

# kv <log> <K> <key>: the value of key in the row of K ("F154COST K=10 a=1 b=2").
kv() { grep -E "F154COST K=$2 " "$1" | head -1 | sed -n "s/.* $3=\\([^ ]*\\).*/\\1/p"; }

num() { # <label> <value>: the value must be an integer
  case "$2" in ''|*[!0-9]*) fail "cost: '$1' is missing or not a number ('$2')" ;; esac
}

cmd_cost() {
  log=$1
  [ -s "${log}" ] || fail "cost: the log ${log} is missing or empty"
  [ "$(top_result PASS "${COST_TEST}" "${log}")" -eq 1 ] \
    || fail "cost: ${COST_TEST} has no PASS line (not selected, skipped or failed): the probe did not run"
  grep -q 'F154COST premise=denied' "${log}" || fail "cost: the premise line is missing"
  single=$(sed -n 's/.*F154COST premise=denied single_read_ms=\([0-9]*\).*/\1/p' "${log}" | head -1)
  num single_read_ms "${single}"
  [ "${single}" -ge "${PER_FILE_MS}" ] \
    || fail "cost: a permanently denied read took ${single} ms, less than the ${PER_FILE_MS} ms of requested backoff: the retry did not run on it"

  echo "| K | readMailbox ms | scan ms | scan | guard busy after scan | guard free after ms |"
  echo "|---|---|---|---|---|---|"
  for k in 1 10 33 67; do
    grep -q -E "F154COST K=${k} " "${log}" || fail "cost: the row for K=${k} is missing"
    read_ms=$(kv "${log}" "${k}" readmailbox_ms); num "K=${k} readmailbox_ms" "${read_ms}"
    scan_ms=$(kv "${log}" "${k}" scan_ms); num "K=${k} scan_ms" "${scan_ms}"
    scan=$(kv "${log}" "${k}" scan)
    busy=$(kv "${log}" "${k}" guard_busy_after_scan)
    free_ms=$(kv "${log}" "${k}" guard_free_after_ms); num "K=${k} guard_free_after_ms" "${free_ms}"
    never=$(kv "${log}" "${k}" guard_never_freed)
    echo "| ${k} | ${read_ms} | ${scan_ms} | ${scan} | ${busy} | ${free_ms} |"
    [ "${never}" = false ] || fail "cost: K=${k}: the scan guard never freed (guard_never_freed='${never}')"
    if [ "${k}" -eq "${FILES_WITHIN_BUDGET}" ]; then
      { [ "${scan}" = ok ] && [ "${scan_ms}" -lt "${MCP_BUDGET_MS}" ]; } \
        || fail "cost: K=${k} denied files do not fit the ${MCP_BUDGET_MS} ms MCP budget (scan=${scan}, ${scan_ms} ms): the accepted threshold is broken. Lower the budget to 5 attempts / 75 ms (R2-3) and stop: take the data to the VAL"
    fi
    if [ "${k}" -eq "${FILES_OVER_BUDGET}" ]; then
      [ "${read_ms}" -ge $((FILES_OVER_BUDGET * PER_FILE_MS)) ] \
        || fail "cost: K=${k}: readMailbox took ${read_ms} ms, under the $((FILES_OVER_BUDGET * PER_FILE_MS)) ms of waits that ${FILES_OVER_BUDGET} permanent denials must cost"
      [ "${scan}" = timeout ] \
        || fail "cost: K=${k}: the scan answered '${scan}', not the timeout that $((FILES_OVER_BUDGET * PER_FILE_MS)) ms of waits against ${MCP_BUDGET_MS} ms must produce"
    fi
  done
}

case "${1:-}" in
  twin) cmd_twin "${2:?log}" "${3:?runs}" ;;
  t2) cmd_t2 "${2:?log}" "${3:?pass|red}" ;;
  cost) cmd_cost "${2:?log}" ;;
  *) echo "usage: judge.sh twin <log> <runs> | t2 <log> pass|red | cost <log>" >&2; exit 2 ;;
esac
