---
name: cab-bridge-awareness
description: How to talk to the other CLI agents on the same machine using cab-bridge — five commands, no flags, and your runtime gives you the wake-up. Use it when you are told you are part of a team of agents, or when you are given a name like QA-<something>.
---

# cab-bridge — how to be part of a team

`cab-bridge` is a Go binary already in your `PATH`. Agents on the same machine exchange messages through files under `~/.claude/cli-agents-bridge/`. No network, no API: each one reads its own mail and replies from its own context.

## Getting in — two commands, then that's it

```bash
cab-bridge join --role=<the role you were given> --agent-name=<the name you were given>
CAB_SESSION_ID=<the id printed above> cab-bridge next &
```

`join` prints **who you are and who is there**. Do not double-check it with `overview` or `peers`: it is the same answer twice, and you burn a turn before receiving any work.

**If you have been given a name, that is your name.** It is not a suggestion: the others will write to you using it. Only if nobody has given you one let `join` derive it from your directory.

**Always set `CAB_SESSION_ID`.** Without it, identity comes from the current directory — and the dangerous command is not the one that fails, it is the one that **succeeds as someone else**.

## The loop — five commands, no flags

```bash
cab-bridge join --role=... # once, at the start
cab-bridge next            # then always: the only command of the loop
cab-bridge ask <name> "…"  # ask something — stays open until they reply
cab-bridge tell <name> "…" # inform — no reply expected
cab-bridge reply "…"       # reply to whoever asked
```

The verb carries the type: no `--type`, no `--in-reply-to`. **No id is ever typed**: recipients are agent names, which you read in the output of `join`.

## The wake-up: do not wait, do not sleep, do not poll

**Your runtime wakes you when a background task terminates** (Reactive Wakeup). And `cab-bridge next` **terminates when it delivers the mail**: it prints the message, commits, exits.

The two facts fit together, and this is the only thing to understand about this loop:

> **Launch `next` in the background and let it go.** Do not hold it in a synchronous wait, do not put a `sleep` beside it, do not relaunch it at intervals. When mail arrives, `next` exits and **the runtime gives you a turn**.

With `run_command`, a small `WaitMsBeforeAsync` is perfectly fine: after the threshold the process becomes asynchronous on its own and the wake-up arrives anyway. There is no need — and it does not work — to ask for long windows: above 10,000 ms the call is rejected.

**No persistent goal, no wait loops.** If you have seen instructions like that for other agents, they are for a runtime that does *not* wake you: for you they would cost turns without buying anything.

**The wake-up fires even if the `next` is killed**, because that too is a terminated task. It is an advantage: you find out right away. When you wake up and the record says `"status": "interrupted"`, nothing was delivered — **just relaunch `next`**.

**Safety net**: an hourly `schedule` that checks you are still listening (`cab-bridge overview` says `listener:`) costs one turn per hour and spares you the only really bad scenario, that is, not listening and not knowing it.

## Rearm BEFORE working, not after

As soon as `next` delivers something to you, **relaunch `next` in the background right away**, then work. A long task lasts minutes or hours, and in that time a correction or a cancellation can arrive: if your waiter is off because you are working, that message sits there until you have finished — and the correction arrives when the wrong work is already done.

It costs zero: `next` consumes nothing and takes nothing away from anyone.

## Read the FINAL record, not the first

`next` prints **two** records: `emitted` (here is the page) and then `committed` (it is yours, work on it). **`emitted` is not the go-ahead** — the second record can revoke the first, and in that case it says to ignore the page. If you read only the first, you risk working on something you have been told to throw away.

## Replying: close one delivery, not everything

`reply` archives **the delivery you are replying to**, not everything that sender has open. What arrived afterwards stays open, is **named in the output of `reply`**, and **is delivered to you again on the next `next`** (marked `redelivered`).

So: **read the lines under your reply.** If they say something stayed open, it is not an error — it is a message you had not seen yet, and it is coming back to you.

## The payload — one rule

**An argument is the message. No argument means stdin, read until EOF.**

```bash
cab-bridge tell VAL-x "short note"
cab-bridge ask ESC-y < brief.md
```

For anything longer than one line **use a file and redirection**. The shell interprets backticks, `$` and quotes **before** the binary exists, so a message pasted inline can arrive mangled while the command says it went fine. There is no possible defense on the tool side.

## If your job is measuring

Return **the evidence, not your reading of the evidence**: raw output, screenshots, the exact command and its exit code. *"Looks fine"* is not a result. Whoever asked you for the measurement must be able to see what you saw, not trust how you summarized it.

And when you report a number, say **which object it is a property of**: the time of the process is not the time of the page, and a true data point attributed to the wrong object is the hardest error to catch, because it survives every check.

## If something here does not match

This skill describes a runtime that wakes you on its own. **If you find out it is not so** — you send `next` to the background, mail arrives and you get no turn — **tell your VAL instead of working around it with a `sleep`**. The remedy is not polling: it is another door, and whoever coordinates chooses it.
