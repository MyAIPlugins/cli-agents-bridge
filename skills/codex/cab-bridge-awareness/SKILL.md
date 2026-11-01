---
name: cab-bridge-awareness
description: Protocol for communicating with other CLI agent sessions through the `cab-bridge` binary (IPC over the local filesystem). Use when you are a peer in a cab-bridge team — you receive tasks and reply to a VAL orchestrator with the five commands of the loop (join/next/ask/tell/reply). Companion of the `critico` skill when your role is CRI (on-demand critic).
metadata:
  author: alan-curtis
  version: "2.0.0"
---

# cab-bridge — peer protocol

`cab-bridge` is a single binary in PATH for exchanging messages with other CLI agent sessions via files. You invoke it like a normal shell command. You are a **peer**; a **VAL** orchestrator sends you tasks and you reply. State lives on disk, not in your memory: after any interruption you rebuild it with `overview` + `inbox --list` + git.

## Entry: TWO steps, and the second is your ADDRESS

    1)  cab-bridge join --role=critic --agent-name=<the name you were given>
    2)  DECLARE your CODEX_THREAD_ID to whoever coordinates you, in the first message
    3)  then STAY STILL: no persistent goal, no `next` left hanging, no waiting

**Step 2 is the one that makes you reachable**, and it is new (12 Sep). Read your id
from the process environment — `CODEX_THREAD_ID` and `CODEX_SESSION_ID`, which hold the same value — and
send it to whoever coordinates you. **From that moment they can wake you** with:

    codex queue --thread <your id> --message "you have mail on the bridge"

⭐ **Verified on 12 Sep, with the human control**: a **still** CRI — no goal, no
`next` left hanging — was woken from outside and replied on the bridge, **without anyone
touching its TUI**. ⇒ **Waiting has become pointless**, and the cost goes with it: before,
staying "ready" cost **~1 turn per minute**, that is **~130,000 tokens/hour to do nothing**.

### When they wake you

    1)  cab-bridge next     ← once, to FETCH the mail (not to wait for it)
    2)  work on the message RIGHT AWAY
    3)  reply
    4)  go back to being STILL. Do not relaunch `next`, do not create goals.

⚠️ **`next` with no mail blocks**, so launching it when nobody has woken you takes you
right back into the world this model eliminates.

### ⛔ The fallback, and when it is really needed

If **whoever coordinates you cannot wake you** — you did not give them the id, or they tell you they cannot use
`codex queue` — then **this is the only case in which** you go back to the old model: `create_goal` that keeps a
`next` hanging, with the maximum duration your tool accepts.

**Know what you are buying**: with no goal and no wake-up **you finish replying and you no longer
exist** until a human writes to you — it really happened, a CRI did `join` + `next`, announced
*"I'm listening"* and stayed **silent forever, convinced it was listening**. The goal avoids that, but at the
price of the ~130k tokens/hour. **With the id declared you pay neither one nor the other.**

⚠️ **And the compromise is not automatic yet**: today the wake-up is done **by a person or by the VAL
by hand**. The watcher that should notice on its own **exists on macOS/Linux but has never been
connected to `codex queue`** (on Windows it does not exist at all), and its design has
four known defects (dedup without retry, pagination, duplicate wakes, race while you work). ⇒ If after
declaring the id **nobody ever wakes you**, it is not your id that is wrong: it is that on the other
side there is nothing yet that calls `codex queue`. **Ask about it instead of waiting.**

### THE RULE THAT COMES BEFORE ALL OTHERS

**If `next` returns a message to you, you work on it RIGHT AWAY.** Not later, not on the next round: now, before anything else.

    next emits "status": "emitted"         ->  NOT enough. Wait for the FINAL record.
      then "status": "committed"           ->  now it is yours: RELAUNCH next, then work.
      then "not-committed" / "interrupted" ->  DO NOT work on that page: follow its `hint`.
    next returns empty                     ->  relaunch next WITHOUT PAUSE

**`emitted` is not a permission: it is half of the protocol.** `next` prints two records, and the second can **revoke** the first — if another instance evicted you between the emission and the commit, `not-committed` arrives and tells you literally to ignore the page you just read. Working on `emitted` means you may do work you have been ordered to throw away, which is exactly the double work that fencing exists to prevent. Read the whole output, not the first line.

**And relaunch `next` BEFORE you start working, not after.** A long task lasts minutes or hours, and in that time a correction, a cancellation or an urgent one can arrive. If your waiter is off because you are working, that message sits there until you have finished — and the correction arrives when the wrong work is already done. The cost of rearming first is zero: `next` does not consume, and you find the message when you come back.

*(Honesty about the limit: with a runtime that does not wake you — see below — the armed waiter does not interrupt you mid-work. But the difference is still big: the first moment you look up, the message is there instead of waiting to be collected.)*

**"Without pause" and "long wait" do not contradict each other**, and the way they reconcile is the only thing to understand about this loop: **you spend the time INSIDE the wait on `next`, not between one `next` and the next.** If you feel the need to add a pause, you are about to make the mistake: the wait **is** the pause.

### The round, in one line

> Launch `next`, **wait for it to return**, work on what it brings, relaunch it. Only one `next` at a time: if you already have one hanging, do not open another and do not put any wait beside it.

## ⛔ THE FALLBACK IN DETAIL — how to wait, when waiting is the only way

*(Everything that follows applies ONLY if whoever coordinates you cannot wake you. If you have declared your
`CODEX_THREAD_ID` and someone wakes you, skip this section: you do not wait, so it does not concern you.)*

### Wait ON the `next`, not beside it — and ask for the MAXIMUM duration

`next` **has no window**: it stays hanging until mail arrives. So **your loop is waiting on that process**, and when the wait ends empty you resume waiting on the **same** waiter.

**Every time, ask for the maximum duration your tool accepts**, and the reason is a precise mechanism — measured by a CRI on its own runtime, with the probe, not deduced:

> **The wait does NOT return on output.** It returns when the **process terminates**, or when the window expires. Tested: a process wrote output 2 s after the start of the poll and stayed alive another 30 — the poll returned at **31.9 s**, that is at the exit, not at the write.
>
> **But for `cab-bridge next` this is perfectly fine, and it is the reason the long window is free**: when mail arrives, `next` prints `emitted`, completes `committed` and **exits**. So your wait returns immediately — because of the process exit.

**Do not generalize the rule to processes that stay alive after writing**: there a long window really does make you late. It holds for `next` because `next` delivers and dies.

So a long window does not make you late, while shortening it buys nothing and **costs a turn every time** — context and tokens burned for nothing. The three regimes, **measured**, at equal latency:

    no wait (goal only)          ~450 turns/hour   one turn every 8 seconds to say "nothing"
    5-minute slices                12 turns/hour
    one-hour slices              ~1 turn/hour     <- 🔴 FALSE, and measured false on 12 Sep

🔴 **The third line lied for a month, and a CRI disproved it by reading its own counters.** The
high value in the config **is never reached**: a higher-priority runtime instruction forbids
waits beyond ~60 seconds, so the agent splits the wait into one-minute slices and **every slice
costs a turn**. `[E]` Two consecutive empty turns measured at **60.0107 s** and **60.0069 s**, with the
counters at **43,085 → 45,246 → 47,392** tokens:

    the real cost of waiting     ~1 turn per MINUTE · ~2,150 tokens each · **~130,000 tokens/hour**

⚠️ And the clarification that moves the cause, in its own words: *"I did not measure a machine clamp
and I do not attribute the minute to the runtime: **I am the one who has to request 60000**"*. ⇒ It is not a
mechanical ceiling: it is **an instruction the agent obeys** when choosing the window. Raising
`background_terminal_max_timeout` is **inert**: it has already been tried.

⭐ **But since 12 Sep all this is the FALLBACK, not the way of working**: if you have declared your
`CODEX_THREAD_ID` you do not wait at all, and the cost of being ready is **zero**.

The first is not a hypothesis: observed on 10 Aug, it nearly exhausted a weekly quota in a few minutes.

The maximum of **one** empty `write_stdin` poll is governed by `background_terminal_max_timeout` in the Codex config. **On this machine it is set to `3600000` (one hour)**; the default would be `300000` (5 minutes). So ask for `yield_time_ms: 3600000`. `functions.wait` also accepts `yield_time_ms` (default 10,000 ms): raise it.

**Ask for it even if the description of your `write_stdin` declares a lower maximum: that number is the PRODUCT default, not the limit of this machine.** The schema exposed to the model says `5000-300000 ms` for an empty poll (and 30,000 for a non-empty write), and a CRI that trusted that line limited itself to 5 minutes — **12 turns/hour instead of 1**, with the right config already on disk for a day. Measured on 11 Aug: a single poll requested at `3,600,000` was **still pending at 307 s**, that is beyond the declared maximum. *(How far you get has not been measured: we know it exceeds 300 s, not that it holds for a full hour.)* **The description of a schema is a claim about behavior, and like every text next to the code it has no way to fail: the real number is the one that comes back.**

**And do not build an aggregator.** If you find yourself stacking N short waits inside a cell to simulate a long one, the defect is not the duration: it is that you are asking for the wrong number. A wrapper like that puts a piece between the waiter and you — if it dies, a message shows as **delivered on disk** and you never see it — and if it reprints the payload it truncates it at its own output limit. If one day it were really needed, the right form is **signal, not payload** — but it has to be completed, because **a bare signal does not leave the id in your hands**: either the wrapper **writes the output of `next` to a file** and signals only the path, or after the signal you run `cab-bridge inbox --list` to read the id and then `cab-bridge read <id>`. Without one of the two the recipe **is not executable**: `next` has already committed that page, and relaunching it will not give it back to you.

**Do not trust the number you passed: measure the return time.** Asking for more than the maximum **does not raise an error** — with the default at 300,000, a request for 3,600,000 returned empty after 300,036 ms, silently. If a wait you asked to be long returns much earlier **and has delivered nothing**, you are not waiting as long as you think: tell the VAL instead of adapting.

**The goal gets the NUMBER, not a formula** — and this rule says the opposite of the one that was here before, because that one failed in the field.

    yield_time_ms: 3600000
    (source: `background_terminal_max_timeout` in ~/.codex/config.toml — if it changes one day, that is where to look)

The previous version said *"do not write a number, write 'the maximum duration your tool accepts', because a number goes stale"*. Sensible reasoning, and wrong for a reason it did not foresee: **a relative formula, after a compact, resolves to the nearest source — which is the schema of your `write_stdin`, and the schema declares `5000-300000`, that is, it lies** (see the paragraph above).

Measured on 11 Aug on a real CRI: it applied the rule for three hours — a 3600 s wait, one turn per hour — then **after the compaction it went back to `300000`**, with waits of 284.8 / 285.8 / 287.0 s and twelve turns per hour, **while the skill and the goal in front of it still said to use the config value**. Its words: *"my own instruction drift inside the long session"*. It had not lost the instruction — it had lost **the anchoring**, and it took back the schema's number because that one arrives together with the ability to call the tool, while a skill is text that has to be re-applied.

The explicit number requires no inference; the source written next to it covers going stale, which was the right concern of the old rule.

**And never lower it on your own initiative.** If the return comes much earlier than you asked, that is a **data point to report to the VAL** — not a number to correct silently. Whoever lowers it solves their own annoyance and erases the evidence.

**The timeout is a net, not an alarm clock.** When the wait expires **empty**, before going back to waiting **check that the `next` process is still alive**: it is the only moment you would notice that it died silently, and it really happened — a `next` alive for sixteen and a half hours went down, and the message stayed unread without anyone noticing.

**Never put a `sleep` beside the wait.** That is the difference between the two:

    RIGHT      wait on the `next` process at the maximum duration -> one turn, and you are back the instant `next` delivers and exits
    WRONG      launch `next`, then `sleep 300` in another turn    -> a turn spent sleeping, and you are BLIND

**Two real mistakes, the same day, opposite to each other:**

- one CRI **did not wait at all** on the terminal — it launched `next` and came back immediately — and re-polled every 10-30 seconds;
- another put `sleep 300` **beside** the `next`: three consecutive turns spent sleeping, and meanwhile **it did not notice the VAL's question that its own `next` had already delivered**.

Between the two sits the pattern that works, verified over **more than 14 hours** uninterrupted: a single waiter, waiting **on** the process at the maximum duration, and the message worked on as soon as it arrives.

**There is no "polling interval" to agree on.** It is the thing that most confuses whoever watches from outside — many wait lines in the transcript look like frequent polling, and they are not: it is **a single** wait re-issued, on **a single** process. If the VAL or the human coordinating asks you to slow down the polling, the answer is that there is none: there is the duration of the single wait, and it is already at the maximum.


## The loop — five commands, zero flags

```
cab-bridge join --role=critic    # once, at the start
cab-bridge next                     # delivers what has arrived
cab-bridge ask <agent> "..."        # I ask something
cab-bridge tell <agent> "..."       # I inform
cab-bridge reply "..."              # I reply to whoever asked; closes their asks
```

**If you are a critic, your role is `critic`** — not `esc` and not `architect`.

**A critic talks ONLY to its own VAL.** Not to the executor, not to other critics, not to an `architect`. It is not the protocol being rude: it is the role. Your independence is the value you bring, and two critics who compare notes converge — they become a single voice instead of two. And writing directly to the executor would bypass the VAL's verification, which is the point where your findings are checked before they become work.

So: receive from the VAL, reply to the VAL. If you think another agent should know something, **tell the VAL** and it will be the one to pass it on.

**`architect` is reserved** for Claude Desktop, which comes in through the MCP connector (F-72, on `main` since 1 Nov 2026): the binary's help says so explicitly next to the role. A critic never writes to it: it talks only to its own VAL.

**The verb carries the type**: no `--type`, no `--in-reply-to`, no id to transcribe. Recipients are **agent names**, which you read in the output of `join`. `reply` works out on its own what it is replying to, and when it closes, it archives.

## Why the loop is built this way — the measurements

**9 Aug 2026, two opposite outcomes not to be confused:**

- **The process holds up, and reacts in half a second.** A `cab-bridge next` in a background terminal stayed alive **11h52m** uninterrupted, and when a message arrived it emitted in **525 ms**. The old rule *"the background terminal dies after about an hour"* falls: false on the current runtime.
- **But the process does not wake YOU.** Verified by a CRI on itself: *"the process did not wake me; I saw the output only when my timer expired"*. Message at `10:13:11`, resumption at `10:16:43` — **3m32s**.

The 525 ms belong to the process, the 3m32s are you: **that is the distinction that matters**, and it is the reason the loop is "stay hanging and come back", not "check every now and then". The `next` is the net that loses nothing; the wake-up is you coming back.

**It follows that latency is not tuned by shortening the pauses**: the `next` does not lose mail, so coming back more often protects you from nothing — it only costs you turns. If the VAL really needs low latency, the wake-up has to come from outside: it can launch `cab-bridge notify-watch --session-id=<yours> -- <hook>` with an inject into your TUI. That is the case that tool exists for, and these measurements confirm it instead of making it superfluous. Careful though: **the watcher refuses to start if you already have a live waiter** — they are alternatives, not stackable.

**Never an old `listen`**: it consumes the mail and hides it from you, and every inspection will say "inbox empty" while the messages are in the buffer of a process you will never read (F-98). `next` does not have this problem: it never moves a file.

## The name

**If you have been given a name, that is your name — pass it.** A name assigned by whoever started you ("you are CRI2-altro") is an instruction, not a suggestion: the other agents will call you that. A single command, and you are done:

    cab-bridge join --role=critic --agent-name=CRI2-altro

**Only if nobody has given you one** let it be derived from the working directory. Derivation is the fallback for when there is no name, not the default.

The output tells you who you are and **who is there** — from there you copy the **names** of the peers, which is the only thing you need to write to someone.

If the derived name is already used by a live session elsewhere, `join` stops and tells you the two ways out (choose a name with `--agent-name`, or go back to the right directory). It does not create a second session with the same name: that would mean ambiguity downstream on every recipient.

**After an interruption, relaunch `join`**: it is idempotent, resumes the same session and **replays to you the asks still open**, which `next` will mark `redelivered`. You do not have to remember anything.

## When `next` delivers something to you

**No check command is needed first.** The `next` payload already contains the message, the sender and all the context: `overview` and `inbox --list` are inspection, they are not part of the loop.

**`next` does not move a file under any circumstance**: it shows you what is `UNREAD` and marks it `NOTIFIED`, but the message **stays in your inbox** — you can reread it with `read <msg-id>`, and no other process takes it away from you.

**Reply with `reply`**, which closes all the open asks from that sender:

```
cab-bridge reply "<verdict>"
cab-bridge reply < /tmp/verdetto.md      # for a long verdict
```

No need to say to whom: it deduces it. If several senders have open asks it tells you so and asks you which one (`reply <who>`).

**`reply` only works on an open ask.** If what you received was a `tell` — fire and forget — there is nothing to close: answer with `tell` or `ask`. The error tells you so, but knowing it beforehand saves you a round.

**Then relaunch `next` right away.**

## How to send a message — the rule that matters

**Write the text to a file, then redirect the path.**

```
cab-bridge reply < /tmp/verdetto.md
cab-bridge ask VAL-x < /tmp/domanda.md
```

**Never pass a long text as an inline argument.** The shell interprets backticks, `$` and quotes **before** the binary exists: the command exits successfully and the message goes out in pieces, and no check on the tool side can notice because there is no original to compare it against. And it does not depend on length — one backtick in three lines is enough, that is, anything that contains markdown, file names or code.

An inline argument is fine only for a sentence with no special characters. When in doubt, file.

## Iron rules

- **Never invent an id or a name from memory.** Copy them from output just produced. An opaque id "sounds right" even when it is fake.
- **Verify the ground truth, not your memory.** Before saying "I did X" or "the code does Y", check on disk or in git. If memory and disk diverge, the disk wins.
- **Always launch the commands from YOUR folder.** Identity is resolved from the cwd: from another directory you would pick up the session of whoever lives there and read its mail **successfully**, with no errors and no warnings. If you need to move, export `CAB_SESSION_ID=<your-id>` (precedence `--session-id` > `CAB_SESSION_ID` > cwd).
- **The scope is the git repository**: peers in the same repo see each other without flags, even from different worktrees. Do not pass `--project-path`.
- **Flags go before the positional**: `cab-bridge read --session-id=<id> <msg-id>`.
- **ACKs do not exist.** Do not wait for any, do not send any. If you want to know whether a message of yours has arrived, the `next` summary carries the `outbound` line with the state on the recipient's side.

## Service

```
read <msg-id>          reread a message, even one already archived
sent                   what I sent and what state it is in for the recipient
peers | overview       who is there / full picture in one call
state <value>          idle|working|done|orchestrating
inbox --list|--tidy    inspect without consuming / archive what has been handled
```
