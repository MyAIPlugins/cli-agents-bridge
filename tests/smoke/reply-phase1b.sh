#!/bin/bash
# Smoke for v0.8 phase 1b (ask / tell / reply): real binary, real processes.
#
# The centrepiece is the INDUCED CRASH between SENT and archiving: the state a
# crash leaves is built on disk, then the real `reply` is asked to recover it.
# It must finish the archiving WITHOUT delivering a second response — the one
# guarantee unit tests can assert but only a real run can demonstrate.
#
#   Usage: tests/smoke/reply-phase1b.sh [path-to-binary] [work-dir]
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

# One scope (git marker at the base), one working dir per agent — the shape of
# real worktrees, and what the verbs need to resolve a recipient by name.
BASE="$ROOT/repo"; mkdir -p "$BASE/.git" "$BASE/val" "$BASE/esc"
VALDIR="$BASE/val"; ESCDIR="$BASE/esc"

pass=0; fail=0
check() { if [ "$2" = "0" ]; then echo "  PASS  $1"; pass=$((pass+1)); else echo "  FAIL  $1"; fail=$((fail+1)); fi; }
countfiles() { ls -1 "$1" 2>/dev/null | wc -l | tr -d ' '; }

echo "== setup =="
VALID=$(cd "$VALDIR" && "$BIN" register --role=val --agent-name=VAL-sm 2>/dev/null | grep -oE '"sessionId": "[a-z0-9]+"' | grep -oE '[a-z0-9]{8}' | tail -1)
ESCID=$(cd "$ESCDIR" && "$BIN" register --role=esc --agent-name=ESC-sm 2>/dev/null | grep -oE '"sessionId": "[a-z0-9]+"' | grep -oE '[a-z0-9]{8}' | tail -1)
echo "  VAL=$VALID  ESC=$ESCID"
[ -n "$VALID" ] && [ -n "$ESCID" ]; check "both sessions registered in one scope" $?

echo "== 1. ask by NAME, no id anywhere =="
( cd "$VALDIR" && "$BIN" ask ESC-sm "please do the thing" > "$ROOT/ask.out" 2> "$ROOT/ask.err" )
check "ask ESC-sm succeeded" $?
grep -q "ESC-sm" "$ROOT/ask.out"; check "echo names the recipient" $?
ASKID=$(grep -oE 'msg-[a-f0-9]{12}' "$ROOT/ask.out" | head -1)

echo "== 2. fail-closed on an unknown name =="
( cd "$VALDIR" && "$BIN" ask NOBODY-HERE "hello" > /dev/null 2> "$ROOT/unknown.err" )
[ "$?" != "0" ]; check "unknown recipient is refused" $?
grep -q "ESC-sm" "$ROOT/unknown.err"; check "the error lists who IS here" $?

echo "== 3. the message body can come from stdin =="
echo "a long body from a pipe" | ( cd "$VALDIR" && "$BIN" tell ESC-sm > "$ROOT/tell.out" 2>&1 )
check "tell with stdin succeeded" $?

echo "== 4. next delivers, reply closes =="
( cd "$ESCDIR" && "$BIN" next > "$ROOT/next.out" 2>&1 )
grep -q '"status": "delivered"' "$ROOT/next.out"; check "next delivered the batch" $?
INBOX="$CAB_DATA_DIR/sessions/$ESCID/inbox"
[ "$(countfiles "$INBOX")" = "2" ]; check "both messages still in inbox (next is pure-read)" $?

( cd "$ESCDIR" && "$BIN" reply "done, gate green" > "$ROOT/reply.out" 2> "$ROOT/reply.err" )
check "reply succeeded" $?
grep -q "closed:" "$ROOT/reply.out"; check "reply echoes what it closed" $?
[ "$(countfiles "$CAB_DATA_DIR/sessions/$VALID/inbox")" = "1" ]; check "exactly one response reached VAL" $?
RESPID=$(ls -1 "$CAB_DATA_DIR/sessions/$VALID/inbox" | head -1 | sed 's/\.json$//')

# The ask is archived; the tell is NOT (a tell is never "open").
[ ! -f "$INBOX/$ASKID.json" ]; check "the ask was archived by reply" $?
[ "$(countfiles "$INBOX")" = "1" ]; check "the tell stayed in inbox — a tell is not closable" $?

echo "== 5. INDUCED CRASH between SENT and archiving =="
# A second ask, delivered and answered — but we rebuild the exact state a crash
# leaves: response already in VAL's inbox, journal at SENT, own inbox untouched.
( cd "$VALDIR" && "$BIN" ask ESC-sm "second brief" > "$ROOT/ask2.out" 2>&1 )
ASK2=$(grep -oE 'msg-[a-f0-9]{12}' "$ROOT/ask2.out" | head -1)
( cd "$ESCDIR" && "$BIN" next > /dev/null 2>&1 )

VAL_BEFORE=$(countfiles "$CAB_DATA_DIR/sessions/$VALID/inbox")
( cd "$ESCDIR" && "$BIN" reply "answer to the second" > "$ROOT/reply2.out" 2>&1 )
check "second reply succeeded" $?
RESP2=$(grep -oE 'msg-[a-f0-9]{12}' "$ROOT/reply2.out" | head -1)

# Rewind to the crash state: put the ask back in inbox and re-create the
# journal as it was just after SENT, with the response already delivered.
PROCESSED="$CAB_DATA_DIR/sessions/$ESCID/processed"
ARCHIVED=$(ls -1 "$PROCESSED" | grep "$ASK2" | head -1)
cp "$PROCESSED/$ARCHIVED" "$INBOX/$ASK2.json"
cat > "$CAB_DATA_DIR/sessions/$ESCID/reply-txn.json" <<JSON
{
  "schemaVersion": 1,
  "responseId": "$RESP2",
  "to": "$VALID",
  "anchor": "$ASK2",
  "closeIds": ["$ASK2"],
  "state": "sent",
  "archivedIndex": 0,
  "timestamp": "2026-08-08T12:00:00Z",
  "content": "answer to the second"
}
JSON
VAL_AFTER_FIRST=$(countfiles "$CAB_DATA_DIR/sessions/$VALID/inbox")

# The retry: a plain `reply` must notice the journal and finish the job.
( cd "$ESCDIR" && "$BIN" reply "this text must be ignored" > "$ROOT/retry.out" 2> "$ROOT/retry.err" )
check "the retry completed" $?
[ "$(countfiles "$CAB_DATA_DIR/sessions/$VALID/inbox")" = "$VAL_AFTER_FIRST" ]; check "NO second response was delivered (idempotent)" $?
[ ! -f "$INBOX/$ASK2.json" ]; check "the retry finished the archiving it had left undone" $?
[ ! -f "$CAB_DATA_DIR/sessions/$ESCID/reply-txn.json" ]; check "journal removed once complete" $?
grep -qi "resuming an interrupted reply" "$ROOT/retry.err"; check "it announced the recovery instead of completing in silence" $?

echo "== 6. nothing to reply to is an explicit refusal =="
( cd "$ESCDIR" && "$BIN" reply "into the void" > /dev/null 2> "$ROOT/void.err" )
[ "$?" != "0" ]; check "reply with no open ask is refused" $?

echo
echo "== RESULT: $pass passed, $fail failed =="
echo "   evidence kept in: $ROOT"
[ "$fail" = "0" ]
