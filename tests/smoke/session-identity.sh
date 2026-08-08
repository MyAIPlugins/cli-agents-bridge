#!/bin/bash
# Smoke for v0.8 phase 1c, voice B: CAB_SESSION_ID as an INPUT.
#
# The setup this protects is several agents working from different directories
# of the SAME repository (CRI in docs/, VAL at the root) rather than from
# separate worktrees.
#
# The dangerous case there is not a command that fails — F-97 makes that one
# readable — but one that SUCCEEDS WRONGLY: an agent running from the repo root
# resolves the session that lives at the root and reads somebody else's mail,
# with nothing anywhere reporting a problem.
#
#   Usage: tests/smoke/session-identity.sh [path-to-binary] [work-dir]
#
# With no arguments it builds the binary and works under a fresh mktemp dir,
# whose path is printed at the end. Nothing is deleted.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

BIN="${1:-}"
if [ -z "$BIN" ]; then
  BIN="$(mktemp -d)/cab-bridge"
  echo "building $BIN ..."
  (cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/cab-bridge/) || { echo "build failed"; exit 1; }
fi

ROOT="${2:-$(mktemp -d)}"
export CAB_DATA_DIR="$ROOT/data"
export CAB_AUTO_GC_HOURS=0

# ONE repository: VAL at the root, CRI in a subdirectory. No worktrees.
REPO="$ROOT/repo"; mkdir -p "$REPO/.git" "$REPO/docs"
VALDIR="$REPO"; CRIDIR="$REPO/docs"

pass=0; fail=0
check() { if [ "$2" = "0" ]; then echo "  PASS  $1"; pass=$((pass+1)); else echo "  FAIL  $1"; fail=$((fail+1)); fi; }

echo "== setup: VAL at the repo root, CRI in docs/ =="
VALID=$(cd "$VALDIR" && "$BIN" register --role=val --agent-name=VAL-id 2>/dev/null | grep -oE '"sessionId": "[a-z0-9]+"' | grep -oE '[a-z0-9]{8}' | tail -1)
CRIID=$(cd "$CRIDIR" && "$BIN" register --role=esc --agent-name=CRI-id 2>/dev/null | grep -oE '"sessionId": "[a-z0-9]+"' | grep -oE '[a-z0-9]{8}' | tail -1)
echo "  VAL=$VALID (root)   CRI=$CRIID (docs/)"
[ -n "$VALID" ] && [ -n "$CRIID" ]; check "both registered in one repo, no worktrees" $?

echo "== 1. WITH CAB_SESSION_ID: CRI stays itself even from the VAL's directory =="
OUT=$(cd "$VALDIR" && CAB_SESSION_ID="$CRIID" "$BIN" whoami 2>/dev/null)
echo "$OUT" | grep -q "$CRIID"; check "resolved to CRI's own session from the repo root" $?
echo "$OUT" | grep -q "$VALID" && IMPERSONATED=1 || IMPERSONATED=0
[ "$IMPERSONATED" = "0" ]; check "did NOT become the VAL" $?

echo "== 2. the environment outranks the directory, and the flag outranks both =="
OUT=$(cd "$CRIDIR" && CAB_SESSION_ID="$VALID" "$BIN" whoami 2>/dev/null)
echo "$OUT" | grep -q "$VALID"; check "env wins over cwd" $?
OUT=$(cd "$CRIDIR" && CAB_SESSION_ID="$VALID" "$BIN" whoami --session-id="$CRIID" 2>/dev/null)
echo "$OUT" | grep -q "$CRIID"; check "explicit flag wins over env" $?

echo "== 3. fail-closed: a bad value is never ignored in favour of the cwd =="
( cd "$CRIDIR" && CAB_SESSION_ID="NOT VALID" "$BIN" whoami > /dev/null 2> "$ROOT/bad.err" )
[ "$?" != "0" ]; check "malformed CAB_SESSION_ID is an error" $?
grep -q "CAB_SESSION_ID" "$ROOT/bad.err"; check "the error names the variable" $?

( cd "$CRIDIR" && CAB_SESSION_ID="deadbe01" "$BIN" whoami > /dev/null 2> "$ROOT/stale.err" )
[ "$?" != "0" ]; check "stale CAB_SESSION_ID (session gone) is an error" $?
grep -qi "does not exist" "$ROOT/stale.err"; check "and says why, instead of silently using the cwd" $?

echo "== 4. WITHOUT the variable: what actually happens from the root =="
# This is the case the variable exists for. Recorded rather than asserted: if it
# still resolves by cwd, that is a KNOWN LIMIT of the no-env setup and must be
# written down, not discovered later.
OUT=$(cd "$VALDIR" && "$BIN" whoami 2>/dev/null)
if echo "$OUT" | grep -q "$VALID"; then
  echo "  NOTE  without CAB_SESSION_ID, a command run from the repo root resolves the ROOT session ($VALID)."
  echo "        For an agent living in docs/ this means silently operating as the VAL — no error, no warning."
  echo "        KNOWN LIMIT of the same-repo setup without the variable: exporting CAB_SESSION_ID is REQUIRED there."
else
  echo "  NOTE  without CAB_SESSION_ID the root command resolved to: $OUT"
fi

echo
echo "== RESULT: $pass passed, $fail failed =="
echo "   evidence kept in: $ROOT"
[ "$fail" = "0" ]
