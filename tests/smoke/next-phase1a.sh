#!/bin/bash
# Smoke for v0.8 phase 1a (`next` + wake cursor): real binaries, separate
# processes, invoked the way an agent invokes them.
#
# Green unit tests prove the logic, not the usage model (LL-10/LL-12). The F-94
# regression this suite guards passed every functional assertion while holding a
# delivered message for the entire wait window — only a real run catches that.
#
#   Usage: tests/smoke/next-phase1a.sh [path-to-binary] [work-dir]
#
# With no arguments it builds the binary and works under a fresh mktemp dir,
# whose path is printed at the end. Nothing is deleted: the work dir holds the
# evidence (payloads, cursor, manifests) and lives under the system temp dir.
# Exit 0 means every check passed.
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

# One scope (git marker at the base), one working dir per agent: the shape of
# real worktrees, and what the v0.8 verbs need to resolve a recipient by name.
BASE="$ROOT/repo"; mkdir -p "$BASE/.git"
VALDIR="$BASE/val"; ESCDIR="$BASE/esc"
mkdir -p "$VALDIR" "$ESCDIR"

# Reap every background `next` on ANY exit, including the failure paths.
#
# Until v0.8 the wait window did this for free: a forgotten listener died at its
# deadline. With the window removed (§2.2 rev. cdb21dc) a stray background
# process is IMMORTAL, and a suite run several times leaves a pile of them —
# fifteen were found after one evening. The window was removed for the agent,
# and nothing had re-examined who else was relying on it: these scripts were.
BG_PIDS=()
reap() {
  for pid in "${BG_PIDS[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
  done
  wait 2>/dev/null
}
trap reap EXIT INT TERM

pass=0; fail=0
check() { # check <description> <exit-status>
  if [ "$2" = "0" ]; then echo "  PASS  $1"; pass=$((pass+1)); else echo "  FAIL  $1"; fail=$((fail+1)); fi
}

echo "== setup: two sessions, disjoint cwd =="
VALID=$(cd "$VALDIR" && "$BIN" register --role=val --agent-name=VAL-smoke 2>/dev/null | grep -oE '"sessionId": "[a-z0-9]+"' | grep -oE '[a-z0-9]{8}' | tail -1)
ESCID=$(cd "$ESCDIR" && "$BIN" register --role=esc --agent-name=ESC-smoke 2>/dev/null | grep -oE '"sessionId": "[a-z0-9]+"' | grep -oE '[a-z0-9]{8}' | tail -1)
echo "  VAL=$VALID  ESC=$ESCID"
[ -n "$VALID" ] && [ -n "$ESCID" ]; check "both sessions registered" $?

echo "== 1. next waits, and while waiting the session is ALIVE (F-95) =="
# exec: without it $! is the SUBSHELL's pid, and killing the shell leaves the
# binary running — which is how the strays survived a trap that looked correct.
( cd "$ESCDIR" && exec "$BIN" next > "$ROOT/next1.out" 2> "$ROOT/next1.err" ) &
NEXTPID=$!; BG_PIDS+=("$NEXTPID")
sleep 3
kill -0 "$NEXTPID" 2>/dev/null; check "next is still waiting (did not exit early)" $?

MANIFEST="$CAB_DATA_DIR/sessions/$ESCID/manifest.json"
grep -q '"waitingSince"' "$MANIFEST"; check "waiting marker published in manifest (F-81 without a deadline)" $?
MPID=$(grep -oE '"pid": *[0-9]+' "$MANIFEST" | grep -oE '[0-9]+')
[ -n "$MPID" ] && kill -0 "$MPID" 2>/dev/null; check "manifest PID is a live process — not STALE while waiting (F-95)" $?

echo "== 2. a message from another process wakes it and it EXITS immediately =="
BEFORE=$(date +%s)
( cd "$VALDIR" && "$BIN" ask ESC-smoke "smoke brief" >/dev/null 2>&1 )
wait "$NEXTPID" 2>/dev/null
ELAPSED=$(( $(date +%s) - BEFORE ))
[ "$ELAPSED" -lt 15 ]; check "next returned promptly after delivery (${ELAPSED}s, F-94)" $?

grep -q '"status": "emitted"' "$ROOT/next1.out"; check "page record says emitted (never certifies delivery)" $?
grep -q '"status": "committed"' "$ROOT/next1.out"; check "a second record reports the commit outcome" $?
grep -q "smoke brief" "$ROOT/next1.out"; check "message content delivered" $?
MSGID=$(grep -oE 'msg-[a-f0-9]{12}' "$ROOT/next1.out" | head -1)
echo "  delivered: $MSGID"

echo "== 3. next is PURE-READ: the file is still in inbox, nothing archived =="
[ -f "$CAB_DATA_DIR/sessions/$ESCID/inbox/$MSGID.json" ]; check "message still in inbox/ (next never moves a file)" $?
[ ! -d "$CAB_DATA_DIR/sessions/$ESCID/processed" ]; check "processed/ was not even created" $?

echo "== 4. the wake cursor recorded exactly that id, 0600 =="
CURSOR="$CAB_DATA_DIR/sessions/$ESCID/wake-cursor.json"
[ -f "$CURSOR" ]; check "wake-cursor.json written" $?
grep -q "$MSGID" "$CURSOR"; check "cursor contains the delivered id" $?
grep -q '"schemaVersion"' "$CURSOR"; check "cursor is schema-versioned" $?
PERM=$(stat -f "%OLp" "$CURSOR" 2>/dev/null || stat -c "%a" "$CURSOR")
[ "$PERM" = "600" ]; check "cursor permissions are 0600 (got $PERM)" $?

echo "== 5. a second next does NOT re-deliver what is already NOTIFIED =="
( cd "$ESCDIR" && exec "$BIN" next > "$ROOT/next2.out" 2>&1 ) &
NEXT2=$!; BG_PIDS+=("$NEXT2")
sleep 4
kill -0 "$NEXT2" 2>/dev/null; check "second next is waiting, not re-delivering" $?
# Not "output is empty": in a shared scope the B-1 guardrail prints a warning
# on stderr before every id-free command (F-91, still open). What must be absent
# is a PAGE — no message may be re-delivered.
! grep -q '"status": "emitted"' "$ROOT/next2.out"; check "second next re-delivered nothing" $?

echo "== 6. a second next elsewhere RECLAIMS wait ownership (single waiter) =="
( cd "$ESCDIR" && exec "$BIN" next > "$ROOT/next3.out" 2> "$ROOT/next3.err" ) &
NEXT3=$!; BG_PIDS+=("$NEXT3")
sleep 4
if kill -0 "$NEXT2" 2>/dev/null; then kill "$NEXT2" 2>/dev/null; RECLAIMED=1; else RECLAIMED=0; fi
[ "$RECLAIMED" = "0" ]; check "the superseded next exited on reclaim" $?
grep -qi "reclaim" "$ROOT/next2.out" 2>/dev/null; check "it said why (reclaim message)" $?

echo "== 7. next takes no flags (§2.2) =="
( cd "$ESCDIR" && "$BIN" next --session-id="$ESCID" > "$ROOT/next4.out" 2>&1 )
[ "$?" != "0" ]; check "next rejects --session-id" $?

kill "$NEXT3" 2>/dev/null
wait 2>/dev/null

echo
echo "== RESULT: $pass passed, $fail failed =="
echo "   evidence kept in: $ROOT"
[ "$fail" = "0" ]
