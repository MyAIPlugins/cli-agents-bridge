# Codex peer: engagement, wake-up, cost of waiting

Reference of `cab-bridge-awareness`, for the VAL that engages or wakes a Codex peer. The measurements and dates are as they were: an environment number expires, so measure it again before building on it.


## Engagement

**If you are engaging a Codex peer**, the first thing you ask it for is **its `CODEX_THREAD_ID`**, not a persistent goal. It stays still, costs nothing, and you wake it: see **"WAKING A CODEX PEER"** below, which is the way of working since 12 Sep.

⚠️ **The old model (persistent goal + hanging `next`) is the FALLBACK**, and you use it only if you cannot wake the peer. Know what you are buying: with no goal **and** no wake-up, a peer that does `join` + `next` and stops **believes it is listening and is not**, and stays mute forever (happened on 9 August); but with the goal it pays **~130k tokens/hour of silence**. **With the declared id you pay neither.**

📌 And if a peer answers once and then goes silent: we used to ask *"do you have the goal?"*. Now the question is **"did you give me your thread id, and did I wake you?"**, because the silence of a peer at rest is **indistinguishable** from that of a broken peer, and the difference lies on your side.


## The cost of a polling Codex

🔴 ⭐ **THE CODEX ROW (of the vendor map, now in `val.md`) SAID "~1 turn/hour with the raised config": IT WAS FALSE, and the cure it promised
does not exist** (12 Sep, measured by CRI at the request of the human, who had seen it cycling).

`[E]` The three data points, from its report:

    asks `write_stdin` for   `yield_time_ms = 3600000`  (the high value, with the config already fixed)
    gets                     the cell returns IMMEDIATELY, and it then calls `functions.wait` at **60 000 ms**
    who makes it return      **itself**: not the process, not the runtime, not the bridge

⇒ **The cause is a higher-level instruction of the Codex runtime** (*"avoid blocking waits longer than
60 seconds"*), which `[E]` **is not in a skill, not in an `AGENTS.md`, and cannot be modified by us**
(searched for on 12 Sep in both places: absent).
⇒ The agent breaks the wait into 60 s chunks, and **every chunk costs a turn** because the persistent
goal generates a new one. The value in the config **is never reached**.

⭐ **THE REAL COUNT, measured from the inside by a CRI running on Codex on 12 Sep at my request**, and it is
the first honest number we have, because an agent can read its own counters and we cannot:

    two consecutive empty turns     write_stdin → process alive, empty output, at 60.0107 s and 60.0069 s
    counters at the three entries   43,085 → 45,246 → 47,392 tokens
    cost per empty turn             **2,161 and 2,146 tokens**  ⇒  ~4,300 tokens in two minutes of silence

⇒ **~1 turn per minute, i.e. ~60 an hour and ~130,000 tokens an hour to do nothing.** The previous
row said *"~30 turns/hour"*: **wrong by a factor of two**, and in the same cell where an even earlier
row had promised a non-existent cure. *This cell has lied twice: the next time someone writes a number
in it, have it measured by whoever is inside the runtime.*

⚠️ **And its clarification is worth more than the number**, because it moves the cause: *"I did not measure a clamp
of the machine and I do not attribute the minute to the runtime: **it is I who must request 60000**"*. ⇒ It is not
a mechanical ceiling that truncates a long wait: it is **an instruction the agent obeys** when
it chooses the window. The difference is not academic: a clamp can only be circumvented from outside, while
an instruction can be named, and one day perhaps relaxed, in the right place. *And it is the question "which
object is this data a property of?" applied well: the cost belongs to the RUNTIME, not to the model;
a more capable model does not touch that line.*

⛔ **So these do NOT help, and must not be retried**: raising `background_terminal_max_timeout` (done,
inert), opening the bridge repo (the limit is upstream), or asking the agent to wait longer
(it cannot).

🟢 **Today this cost is not paid, because no Codex waits with a goal any more**: with the 12/09
method (thread id + `codex queue`) an **idle Codex peer consumes no tokens** (observed over
dozens of sessions, confirmed on 11/11; the numbers above measure the goal, not the idle peer).
⇒ Keeping it open between tasks is free: whether to keep it or close it is decided by **how relevant
its context is to the next task**, not by cost. ⚠️ The wake-up is still needed: an idle peer does not
know mail has arrived, and its silence is indistinguishable from a broken peer's.
⇒ In practice, with the peer idle: **write to it with `ask`, not with `tell`.** An ask that does not close stays
open and the `next` on wake-up **redelivers** it (`redelivered`); a `tell` that its listener has
already marked `NOTIFIED` while it was asleep does not reappear on its own. *(`[L]`, deduced from the mailbox model,
not yet measured: `ask` is the right form for a task anyway, so it costs nothing.)*


## 🟢 WAKING A CODEX PEER: proven on 12 Sep, and it changes how you engage one

**You no longer need to choose between "ready and expensive" and "off and slow".** The peer stays **still** (no
goal, no hanging `next`, **zero cost**) and you wake it when there is material:

    codex queue --thread <its CODEX_THREAD_ID> --message "you have mail on the bridge"

(It needs codex-cli 0.154.0 or later in the PATH of whoever wakes: the check is in the core, "Peers without native push".)

**How you get the id**: **you ask it, and it reads it from its own environment**: `CODEX_THREAD_ID` and
`CODEX_SESSION_ID`, which match the `thread_id` of its rollout file. ⛔ **Do not look it up yourself with
`codex agents`**: it answers `stdin is not a terminal`, because it queries an **app-server daemon**, and
`~/.codex/sessions/` contains saved rollouts (dead ones included), not who is alive right now.

`[E]` **The test that proves it, and that must be repeated identically if one day you have doubts**: the peer was **still**,
I woke it from outside, **and no human touched its TUI**. *A wake-up that happens only
when someone types is indistinguishable from no wake-up at all*: without that control the test is worthless.

### ⚠️ What is NOT solved yet: read this before you trust it

- **A PERSON wakes it**, not a mechanism, and the reason **is not that the tool is missing**:
  `[E]` on macOS/Linux `notify-watch` **exists** (`cab-bridge notify-watch --help`, verified on macOS on
  13 Sep; on Windows it does not exist), but **nobody has ever connected the watcher to the Codex wake-up**: the
  chain `notify-watch --on-message "codex queue --thread <id> --message …"` is **plausible and NEVER TESTED**, and the
  four design flaws (below) remain open.
  ⇒ If you engage a Codex and then forget to wake it, **it stays mute forever**, and from its side
  that is indistinguishable from "the VAL has not finished yet". **Tell it when you engage it.**
- 🟢 **`queue` while it works: MEASURED on 13 Sep, it is FIFO.** `[E]` A wake sent 46 s into
  a turn lasting 3m56s **does not show up during the work**: it arrives as a new user turn **at the end**,
  **with the text intact**, after ~3m10s in the queue. **It does not interrupt and it is not lost.** Three concordant sources: the
  file timestamps, the agent's report, its TUI. ⚠️ **n=1**: the observed order is consistent
  with queueing, it does not prove the runtime's general policy.
  ⇒ **You can queue work for a busy Codex**, and it sees it when it finishes; waking it **while at rest**
  remains the best for *latency*, not for delivery.
  ⭐ And what brought it back to the bridge **before** the wake was not the wake: it was the **`reply`**,
  which flags unread mail. **The safety net is the bridge, not the external wake-up.**
- **Does the id survive `/clear`, compact, resume?** Deduced from the docs, **not tested**. ⭐ *A UUID
  that is still valid identifies a HISTORY, it does not certify that someone is RUNNING it.*
- **Anyone on the machine** who knows the id can wake that session.

📌 And the four design flaws of an automatic watcher, already found by a critic: dedup that
treats `exit 0` as definitive success (**no retry**), pagination that leaves without a wake-up
everything beyond the first page, duplicate wakes that respawn a blocking `next` on
an empty inbox, and the race between "I'm working" and "mail has arrived". ⇒ **Principle: REPEATABLE
notifications, not an exactly-once claim that cannot be sustained.**

⚠️ And the lesson about the document: that row promised a cure (`with the raised config`) that **nobody
had verified on the current runtime**. The human took it apart by watching the TUI cycle, not by reading.
⭐ A measurement from **9-10 August** went on describing the cost of a runtime that had changed
in the meantime: *an environment number expires like an inventory count, and makes no noise when it dies.*


## The control test for a new peer

**The bridge is identical for everyone**: only the runtime changes, and with it the port. Do not add logic to the bridge to compensate for a vendor's runtime: add an external port. And before promoting a new peer, run the **control test**: you send it a message and **no human touches its TUI**. A wake-up that happens only when someone types is indistinguishable from no wake-up at all, and that is exactly how we discovered that Codex was not waking up.


## Watchers and consumers on the same inbox

*Note of 8 Nov: this paragraph predates 12 Sep. Today a Codex is woken with `codex queue` (above); `notify-watch` remains the port for an automatic wake-up, never tested with Codex.*

Claude Code has native push (a background `next` wakes the agent when it returns). Codex CLI does not: the wake-up must come from outside, with `cab-bridge notify-watch --session-id=<id> -- <hook argv>`, i.e. **non-consuming** polling + hook.
**Do not leave a consumer on the same inbox**: the watcher refuses to start, and that is correct. And if a no-push peer keeps a background `next` that it never reads, that process eats its mail and every inspection will say "empty inbox": seen in the field (F-98).

*Correction of 8 Nov (CRI2, measured in a sandbox): after F-109 the mail does not disappear. The message stays in the inbox marked `NOTIFIED` (`inbox --list` and `ls` show it, the sender's `sent` says `notified`), but `overview` says "nothing unread" and no `next` redelivers it. The rule stands: no orphan `next`s.*
