---
name: cab-bridge-awareness
description: "Operational use of cab-bridge for the VAL role (orchestrator) and for whoever coordinates multiple agents — the five-command loop (join/next/ask/tell/reply), the mailbox model where next never consumes and only reply archives, the single sending method, the authority rule, structural re-arming, and ground-truth verification. Use when you coordinate two or more agent sessions on the same machine."
---

# Operational cab-bridge

The binary is on `$PATH`. Sessions live in `~/.claude/cli-agents-bridge/sessions/<id>/`.

## Entry: TWO commands, and that's it

    1)  cab-bridge join --role=val --agent-name=<the name given to you by whoever coordinates you>
    2)  CAB_SESSION_ID=<the id printed at step 1> cab-bridge next     (in background)

Step 1 prints **who you are and who is here**: do not re-derive that information with `overview`, `peers` or `inbox --list`. It is the same answer twice, and a fresh agent that re-checks it burns turns before it has even received a task.

**If you are the VAL**: `cab-bridge state orchestrating` (once) exempts you from the heartbeat, because between one message and the next you are not in `next`.

**If you are engaging a Codex peer**, the first thing you ask it for is **its `CODEX_THREAD_ID`** — not a persistent goal. It stays still, costs nothing, and you wake it: see **"WAKING A CODEX PEER"** below, which is the way of working since 12 Sep.

⚠️ **The old model — persistent goal + hanging `next` — is the FALLBACK**, and you use it only if you cannot wake the peer. Know what you are buying: with no goal **and** no wake-up, a peer that does `join` + `next` and stops **believes it is listening and is not**, and stays mute forever (happened on 9 August); but with the goal it pays **~130k tokens/hour of silence**. **With the declared id you pay neither.**

📌 And if a peer answers once and then goes silent: we used to ask *"do you have the goal?"*. Now the question is **"did you give me your thread id, and did I wake you?"** — because the silence of a peer at rest is **indistinguishable** from that of a broken peer, and the difference lies on your side.

**The vendor map, all measured on 9-10 August on the same bridge** (latency = from the file on disk to the listener re-arming, i.e. **the model**, not the process):

    Claude Code    native wake-up            run_in_background, zero cost on empty waits
    Codex          🟢 EXTERNAL WAKE-UP       `codex queue --thread <id>` — PROVEN 12 Sep, with
                   (since 0.154.0)           the human control: agent STILL, zero cost while it waits
                   ⚠️ but a PERSON wakes     if instead it POLLS: ~60 turns/hour — ONE PER MINUTE — at ~2,150
                   it: nobody has yet        tokens each ⇒ ~130k tokens/hour of silence
                   connected a watcher
                   to `codex queue`
    Antigravity    native wake-up            3.1s · zero cost · no goal, no waiting
                   (Reactive Wakeup on background task completion)
    Claude Desktop no shell                  comes in through the MCP connector (F-72, on main since 1 Nov 2026)
                   (Cowork: agent in the     after the VAL's `desktop open`; reads mail when
                   cloud, 60 s per tool)     it calls `next`, i.e. inside one of its own turns

🔴 ⭐ **THE CODEX ROW SAID "~1 turn/hour with the raised config": IT WAS FALSE, and the cure it promised
does not exist** (12 Sep, measured by CRI at the request of the human, who had seen it cycling).

`[E]` The three data points, from its report:

    asks `write_stdin` for   `yield_time_ms = 3600000`  (the high value, with the config already fixed)
    gets                     the cell returns IMMEDIATELY, and it then calls `functions.wait` at **60 000 ms**
    who makes it return      **itself**: not the process, not the runtime, not the bridge

⇒ **The cause is a higher-level instruction of the Codex runtime** — *"avoid blocking waits longer than
60 seconds"* — which `[E]` **is not in a skill, not in an `AGENTS.md`, and cannot be modified by us**
(searched for on 12 Sep in both places: absent).
⇒ The agent breaks the wait into 60 s chunks, and **every chunk costs a turn** because the persistent
goal generates a new one. The value in the config **is never reached**.

⭐ **THE REAL COUNT, measured from the inside by a CRI (gpt-6-astra) on 12 Sep at my request** — and it is
the first honest number we have, because an agent can read its own counters and we cannot:

    two consecutive empty turns     write_stdin → process alive, empty output, at 60.0107 s and 60.0069 s
    counters at the three entries   43,085 → 45,246 → 47,392 tokens
    cost per empty turn             **2,161 and 2,146 tokens**  ⇒  ~4,300 tokens in two minutes of silence

⇒ **~1 turn per minute, i.e. ~60 an hour and ~130,000 tokens an hour to do nothing.** The previous
row said *"~30 turns/hour"*: **wrong by a factor of two**, and in the same cell where an even earlier
row had promised a non-existent cure. *This cell has lied twice: the next
time someone writes a number in it, have it measured by whoever is inside the runtime.*

⚠️ **And its clarification is worth more than the number**, because it moves the cause: *"I did not measure a clamp
of the machine and I do not attribute the minute to the runtime: **it is I who must request 60000**"*. ⇒ It is not
a mechanical ceiling that truncates a long wait — it is **an instruction the agent obeys** when
it chooses the window. The difference is not academic: a clamp can only be circumvented from outside, while
an instruction can be named, and one day perhaps relaxed, in the right place. *And it is the question "which
object is this data a property of?" applied well: the cost belongs to the RUNTIME, not to the model —
a more capable model does not touch that line.*

⛔ **So these do NOT help, and must not be retried**: raising `background_terminal_max_timeout` (done,
inert), opening the bridge repo (the limit is upstream), or asking the agent to wait longer
(it cannot).

🟢 **The lever that works today**: **a Codex peer is not kept listening when it has no task.** It
is reactivated when there is material — turning it back on costs much less than ~130k tokens an hour of silence.
⇒ And whoever orchestrates should know this **before** engaging it "so it's ready": with Codex, *ready* is paid for.
⇒ In practice, with the peer paused: **write to it with `ask`, not with `tell`.** An ask that does not close stays
open and the `next` on wake-up **redelivers** it (`redelivered`); a `tell` that its listener has
already marked `NOTIFIED` while it was asleep does not reappear on its own. *(`[L]`, deduced from the mailbox model,
not yet measured: `ask` is the right form for a task anyway, so it costs nothing.)*

## 🟢 WAKING A CODEX PEER — proven on 12 Sep, and it changes how you engage one

**You no longer need to choose between "ready and expensive" and "off and slow".** The peer stays **still** — no
goal, no hanging `next`, **zero cost** — and you wake it when there is material:

    codex queue --thread <its CODEX_THREAD_ID> --message "you have mail on the bridge"

**How you get the id**: **you ask it, and it reads it from its own environment** — `CODEX_THREAD_ID` and
`CODEX_SESSION_ID`, which match the `thread_id` of its rollout file. ⛔ **Do not look it up yourself with
`codex agents`**: it answers `stdin is not a terminal`, because it queries an **app-server daemon**, and
`~/.codex/sessions/` contains saved rollouts (dead ones included), not who is alive right now.

`[E]` **The test that proves it, and that must be repeated identically if one day you have doubts**: the peer was **still**,
I woke it from outside, **and no human touched its TUI**. *A wake-up that happens only
when someone types is indistinguishable from no wake-up at all*: without that control the test is worthless.

### ⚠️ What is NOT solved yet — read this before you trust it

- **A PERSON wakes it**, not a mechanism — and the reason **is not that the tool is missing**:
  `[E]` on macOS/Linux `notify-watch` **exists** (`cab-bridge notify-watch --help`, verified on the Mac on
  13 Sep; on Windows it does not exist), but **nobody has ever connected the watcher to the Codex wake-up**: the
  chain `notify-watch --on-message "codex queue --thread <id> …"` is **plausible and NEVER TESTED**, and the
  four design flaws (below) remain open.
  ⇒ If you engage a Codex and then forget to wake it, **it stays mute forever** — and from its side
  that is indistinguishable from "the VAL has not finished yet". **Tell it when you engage it.**
- 🟢 **`queue` while it works: MEASURED on 13 Sep — it is FIFO.** `[E]` A wake sent 46 s into
  a turn lasting 3m56s **does not show up during the work**: it arrives as a new user turn **at the end**,
  **with the text intact**, after ~3m10s in the queue. **It does not interrupt and it is not lost.** Three concordant sources: the
  file timestamps, the agent's report, its TUI. ⚠️ **n=1** — the observed order is consistent
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
had verified on the current runtime**. The human coordinating took it apart by watching the TUI cycle, not by reading.
⭐ A measurement from **9-10 August** went on describing the cost of a runtime that had changed
in the meantime: *an environment number expires like an inventory count, and makes no noise when it dies.*

**The bridge is identical for everyone**: only the runtime changes, and with it the port. Do not add logic to the bridge to compensate for a vendor's runtime — add an external port. And before promoting a new peer, run the **control test**: you send it a message and **no human touches its TUI**. A wake-up that happens only when someone types is indistinguishable from no wake-up at all, and that is exactly how we discovered that Codex was not waking up.

Field data (9 Aug): three fresh agents spent **six commands each** where two were enough — `join`, then `overview`, then `inbox --list`, then `peers --all-scopes`, then two `--help` — looking for confirmation of things `join` had already said. *(A **pre-F-116** episode: today `peers --all-scopes` is no longer redundant — it is the only way to see the other repositories, which `join` does not show. The other four remain duplicates.)*

## The loop — five commands, zero flags

```bash
cab-bridge join --role=val      # once, at the start
cab-bridge next                 # then forever: the only command in the loop
cab-bridge ask <agent> "..."    # I ask — stays open until they answer
cab-bridge tell <agent> "..."   # I inform — does not wait for an answer
cab-bridge reply "..."          # I answer whoever asked; closes THAT delivery
```

**The verb carries the type.** No `--type`, no `--in-reply-to`, no id to transcribe. Recipients are **agent names**: the ones in your repository you read in the output of `join`, which shows **only your scope** (`join.go:111`). `reply` finds on its own what it is answering.

**For an agent in ANOTHER repository** (F-116, v0.8.0): `<name>@<project>`, where `<project>` is the `SCOPE` column of `peers --all-scopes` — **copy it as printed, and do not add quotes of your own**:

    cab-bridge tell VAL-altro@altro-progetto < /tmp/msg.md

**The previous rule said "always quote it", and it was insufficient.** It covers the space and fails on the apostrophe: a repository called `O'Brien's Tools` closes the quote you opened, and the rest of the line goes back to being shell syntax (`zsh: unmatched '`). It is a directory name someone may really have.

**Now the tool does it**: since v0.9.0 `peers` renders the `SCOPE` column already safe as a single argument — **only where needed**, so the ordinary case is identical to before — and `peers --json` stays raw for whoever parses it. If you add your own quotes on top of a value already rendered, you **break** it.

🟢 **F-135 HAS BEEN CLOSED since the afternoon of 12 Sep — `fromAddressShellArg` verbatim works on Windows too,
from binary `v0.9.0-28-g9298628`.** `[E]` Verified in both directions: the bridge's VAL sent a
message **with the path** and I sent one back **with the path**, both arrived.
⇒ **The workaround below is NO LONGER needed**, and it is kept only so that the error's signature stays
recognizable if one day someone works on an older binary.
⛔ **Before applying it, check the version**: `cab-bridge version`. Below `-28-g9298628` it applies; above
it does not.

<details>
<summary>The signature of the defect, for whoever runs an earlier binary</summary>

🔴 ⛔ **ON WINDOWS `fromAddressShellArg` DID NOT WORK — the project NAME was used instead** (12 Sep, F-135).
`[E]` `next` delivers as the sender's address the **absolute path**
(`'VAL-bridge@<this-repo>'`). Used **verbatim, as this skill prescribes**, the
`tell` fails:

    no agent named "VAL-bridge" in project "<this-repo>"
      — projects with agents: altro-progetto, **cli-agents-bridge**

⇒ The binary **lists among the valid projects the very one it has just rejected**: that is the signature.
`[L]` The cause (from the bridge's VAL): `recipient.go:72` decides *"is it an absolute path?"* with
`strings.HasPrefix(hint, "/")` ⇒ on Windows `C:\…` **does not start with `/`**, ends up in the basename
branch, and `Base()` will never match the full path.

🟢 **Until the binary is updated, use `<name>@<PROJECT-name>`** — the `SCOPE` column of
`peers --all-scopes`, not the path.
⚠️ **It is not universal**: the bare name is a basename, so with two repos with the same final folder
(`…/a/payload` and `…/b/payload`) it becomes ambiguous — and the way out intended for that case is
**precisely the path**, i.e. the broken branch. Today it does not bite because the basenames in use are distinct.
📌 On Unix the original line remains **true**: the `HasPrefix("/")` branch works. It is a
Windows-only defect.
📌 Fix in `feat/windows-lotto1b`, **merged and installed on 12 Sep** (`v0.9.0-28-g9298628`).

</details>

⭐ **And the lesson that remains, when the defect is gone**: between the `[E]` of a report and the
binary the agents actually use there are **push → CI → merge → REINSTALL**, and each of the four
can stall. `[E]` Here the fix had been written **for nine days** and was stuck: what brought it into PATH was a
real case that ran into it, not its correctness.
⇒ **Merged is not installed**, and the only thing that counts is `cab-bridge version`.

*The principle, which holds beyond this field*: **a quoting algorithm is not put in the reader's hands.** No rule applied by hand holds up in every case — we wrote one that seemed complete, and an apostrophe took it apart.

**Cross-project is `val` -> `val`, BOTH sides** (`verbs.go:283`): it is not enough for the writer to be a `val`, the receiver must be one too. So a `critic` that has something for the other repo passes it to its own VAL, and it is the VAL who talks to the VAL over there.

**The reply branch is deliberately exempt**: if a cross-project ask has reached you, a **bare** `reply` goes back on its own — it infers the sender, it needs no address. It is a one-shot capability on that ask, not a channel that stays open: to write to them **yourself** first later on you need the qualified token again, and `next` delivers **two** to you: **`fromAddressShellArg` is the one to paste into a command**, already safe as a single argument; `fromAddress` is the same raw value, for comparing or parsing. **Do not add quotes to either** — if the project contains an apostrophe, the quotes you add break, and on the first one they are already there anyway.

## Mental model — read this first

Four states, and **only one command moves a file**:

| State | Meaning |
|---|---|
| `UNREAD` | arrived, never shown |
| `NOTIFIED` | `next` showed it to you — **it is still in your inbox** |
| `REQUEUED` | it had been shown to you, you did not close it, **it is coming back to you** |
| `ARCHIVED` | closed, moved to `processed/` |

- **`next` does not move a file under any circumstances.** Waking up and consuming are separate acts: that is the point of the whole model. No process can eat an agent's mail any more.
- **Only `reply` archives, and it archives ONE delivery** (F-109, 10 August): it closes the asks that sender sent you **in a single page of `next`**, not everything they have open. What arrived later stays open, is **named under your reply**, and **goes back in the queue**: the next `next` redelivers it to you marked `redelivered`. Confirmation remains a side effect of the work, never a ritual — but the work you confirm is the delivery that was shown to you.
  *Why*: `NOTIFIED` means **"the `next` process printed it"**, not "you read it". You re-arm before working (correct), and a message that arrives **while you are writing** is `NOTIFIED` without you having seen it — before, your reply closed it anyway, and a *"stop, do NOT do A"* could end up answered by a *"did A as asked"*. A real incident, not a hypothesis.
  *Declared limit*: without a read ACK it is not impossible by construction — the oldest page may be exactly the one you have not read. What is guaranteed is **at most one delivery per reply** and **never silently**: you see what you closed and what remains, the sender sees `closes` on the reply and `requeued` in `sent`.
  **So read the lines under your reply.** If they say something was left open, it is not an error: it is a message you had not seen yet, and it is coming back.
- **`next` has no window**: it waits until something arrives. If it is interrupted it says so (`"status":"interrupted"`), and the record carries your open `outbound` items.
- **`emitted` is NOT the green light.** `next` prints **two** records: the page (`emitted`) and then the outcome (`committed` / `not-committed`). The second can **revoke** the first — if another instance evicted you between the two, `not-committed` tells you to ignore the page you just read. Read the whole output, and work only on `committed`. This also applies when you forward it to another agent: if you send them the content of an unconfirmed page, you make them do work that will be thrown away.
- **After a compact**: rerun `join` — it replays your asks that are still open, and `next` marks them `redelivered` inline. Treat them normally.
- **There are no ACKs.** To know whether a brief has arrived, the `next` summary carries the `outbound` line: to whom, for how long, in what state on their side.

## SINGLE SENDING METHOD — `Write`, then redirect the PATH

    1) Write  -> /tmp/msg.md          (the content does NOT touch the shell)
    2) cab-bridge ask <agent> < /tmp/msg.md

**Why `Write` and not an inline message**: content written with `Write` travels as a **parameter of a tool call** and never passes through an interpreter. The shell sees only the PATH, and a path contains no backticks. It is the only chain in which the text does not meet an interpreter at any point.

Fallback if you are already inside bash: **heredoc with a QUOTED delimiter** (`<<'EOF'`), which disables all interpretation. But `<<EOF` without quotes interprets everything: one character of difference, the same silent failure. That is why it is a fallback, not the method.

**Never** pass the text as an argument, not even for two words. One method only: nothing to decide, no way to get it wrong.

**And it applies to EVERY command that takes text, not just the bridge.** The case that got me after I had written this rule: `git commit -m "... ~0.6ms su `+'`peers`'+` con 4 sessioni ..."` — the shell executed `peers` as a command and the message ended up in the history with a hole in its place, already pushed and not rewritable. **For commit messages: always `git commit -F <file>`** (written with `Write`), or a heredoc with a quoted delimiter. The risk does not belong to the bridge: it belongs to any text that passes through a shell line.

**Why "be careful" is not enough.** The shell interprets backticks, `$` and quotes **before** the binary is invoked: when `cab-bridge` starts the content is already corrupted, and **no tool-side check can notice** — there is no original to compare it with. The command exits successfully and the message leaves in pieces.

**It does not depend on length.** One backtick in three lines is enough. The correlation is with the CONTENT — markdown, file names, snippets — i.e. everything a technical agent writes.

**The data that produced the rule (8 Aug):** six messages sent mangled or empty in a single session, all from the VAL, all while it was coordinating the arc that eliminates that defect. One with EMPTY content, delivered normally; in another a backtick invoked `join`, the Unix command. The VAL had known since the morning and fell for it again four times. **Discipline did not work: only removing the choice works.**

If you receive a message with holes where there should be file names or commands, this is it — ask for it to be resent, do not guess.

## AUTHORITY RULE — the VAL dismisses nobody

**Only the human coordinating closes a session.** The VAL does not have the power to dismiss ESC or the critics, and must not propose it on its own initiative.

The recurring defect: the VAL sees the task it ASSIGNED come to an end and concludes that the work is finished — but only the human coordinating knows the complete plan, and almost always the next steps exist and have not been communicated yet. **"They have finished the task I gave them" does NOT mean "they have finished".**

Real error (8 Aug): to the question "when can I deactivate the CRIs?" the VAL answered "now, both of them". They were needed right afterwards and produced 2 P0s and 7 findings, including defects the green gate could not see.

In practice:
- **Never** say "you can switch them off", "they are no longer needed", "we are done". Say instead: *"CRI has no task assigned; if you have something else planned I can engage it on X, otherwise it stays available."*
- An inactive agent **costs nothing**; turning it back on costs context and time. The asymmetry says leave it alive.
- If you do not know what the next steps are, **ask**. The absence of instructions is not a signal of completion.
- It also applies to your own listening: do not stop listening because "everything seems closed".

## RE-ARM BEFORE READING, in the same call

The orchestrator's most frequent defect is not technical: **it forgets to start listening again** and loses messages without noticing. ESC and the critics do not have it, and not through merit: their loop is closed (receive → work → reply → re-arm) and re-arming is the obvious last action of a single sequence. The VAL, between one message and the next, reads, decides, updates docs, commits, writes to multiple agents: re-arming becomes ONE of the N things. **It is a defect in the structure of the role, not in attention.**

> As soon as the notification arrives that `next` has terminated, the FIRST command is the re-arm, and it must be issued in the **SAME tool-call block** as reading the message. Never read first and re-arm afterwards.

Corollary: if you realize you are not listening, **re-arm before doing any diagnosis**. The diagnosis does not expire, incoming messages do.

**And even before that: pin `CAB_SESSION_ID`, always.** The VAL's work requires entering other agents' directories — reading a worktree's `git log`, inspecting a critic's repo — and **the shell's cwd persists from one call to the next**. From that moment every id-free command is no longer yours: it resolves the session of whoever lives there, **succeeding**, with no errors and no warnings.

Real error (8 Aug, twice in the same evening): a `cd` into ESC's worktree for a `git log`, and the next three "re-arms" armed a `next` on **ESC's session**, stealing its ownership of the wait — while I believed I was listening on mine. The first time I had even declared a healthy feature broken because I was looking for my messages in someone else's inbox.

    CAB_SESSION_ID=<your-id> cab-bridge next

`next` accepts no flags, so the environment variable **is the only way** to pin the identity — and it is exactly the case it was built for. It is not a paranoid precaution: it is the structure of the role, like re-arming. Whoever has a closed loop in a single directory does not need it; the orchestrator, who goes through other people's directories as part of the job, does.

**And do not truncate the messages you read.** If you use a script to extract the `content` from the `next` payload, do not put a character limit on it: a long verdict arrives half-way and you act on half. Happened on 9 Aug — four findings of a NO-GO, I read two and forwarded those to the executor as if they were all of them; the two lost ones contained the best. **For a long message use `cab-bridge read <msg-id>`**, which prints everything.

**And do not pipe `next` into `tail` or into a pipe that buffers.** Real error (8 Aug): a background `next` with `| tail -60` held back two reports for twenty minutes — `tail` emits nothing until it receives EOF. The VAL read an empty file and concluded that a critic had not worked. Redirect directly, without filters.

## Verify the ground truth, not the account

**The mother lesson.** An account — yours, an agent's, a tool's — is not the state of the system. Before declaring something, look at the disk: `ls` on the inbox, `git log`, the file.

It applies especially **to the VAL**: in a single evening I declared a healthy component broken, got two counts wrong in the same account, and concluded that a critic had not worked while its report had already been on my disk for twenty minutes. **The disk beats the account even when the account belongs to whoever holds the gate.**

Corollary for plans: a claim about the code's behavior inside a plan ("X holds the lock") must be **verified on the code** before ratifying it, never praised on trust.

**And the flip side, which is just as seductive: the RED that gets explained away.** A failure that disappears on the second attempt **does not say "environment"**: it says *non-deterministic* — and non-determinism, seen from outside, is exactly what a race looks like. The sentence *"a red that disappears when the environment changes is environment, not code"* would classify as contention a defect that shows up only under load, i.e. the most expensive category to find later. **What tells them apart is in the log, and looking costs nothing**: real contention = the process *was making progress* and was killed by the cap; real defect = dead, or hanging **without making progress**. After two days spent on *"green that proves nothing"*, this is the same structure reversed — the explanation is plausible, so it does not get verified.

**An environment note that makes the gate less valid, not just slower** (10 Aug): the machine has **8 logical cores**, and three agents compiling together push the load above 10 — each sizes its own workers on the number of cores, so three builds are ~24 slots on 8. The mechanism, however, **is not** what it seems, and I had written the wrong version here: TSan does **not** detect races by observing two accesses that collide in time — it builds a **happens-before** relation and flags two accesses that are not ordered with respect to each other even seconds apart. Contention does not hide a race in that sense. What it really does: *(1)* tests with a **timeout** do not get to complete, so **the code that contained the race is not executed** — and what is not executed is not analyzed; *(2)* time-dependent tests become flaky, i.e. spurious reds that cost more than the gate. **The conclusion holds — a gate that runs alone is a better gate — but for the second reason, not the first.**

So: if a red gate happens while others are building, **rerun it alone before believing it**. And the right lever is **`-p`, not `GOMAXPROCS`**: `-p` limits how many *test binaries* run together (separate processes, each with its full GOMAXPROCS, **same internal concurrency**), while lowering `GOMAXPROCS` really does take interleaving away from the exploration. The default of `-p` is GOMAXPROCS, so **a single gate already launches eight binaries on eight cores**: it does not take three of you for a gate to assume it owns the machine.

**Order of magnitude, measured twice** (ESC and I independently): the full gate is **~10.8 s wall-clock, ~9 s of CPU**. The VAL had written "~40 s" from memory: off by a factor of four, and on that number it had built the idea of being the dominant consumer. It is not — what saturates the machine are the **builds** (`next`/turbopack/`tsc`), which last **minutes**.

### The four questions, before writing any claim

Born from ten errors of mine in one day. They are not good intentions: they are four questions with an answer, to ask yourself **before** pressing enter on a brief, a document or a message to the human.

**1. Did I EXECUTE it or DEDUCE it?** And say so explicitly, always, even when the deduction is good. I wrote twice that a residual security risk was "narrow", with a correct argument (*SC-7 keeps the base at 0700, so the tree is frozen*) that answered **a different question** — it said nothing about a link planted BEFORE, i.e. the exact window the check existed for. Reproduced in thirty seconds by a reviewer. **A plausible argument is not a verification, and the difference only shows by trying.** If you did not execute it, write that you did not execute it.

**2. WHICH OBJECT is this data a property of?** The number can be true, clean and repeatable, and concern the wrong thing. Three times in one day: 525 ms was the latency of the **process** and I attributed it to the **model**; "the terminal dies after an hour" was true on **one version** and I took it as a property of the runtime; "the cadence emerges on its own" was **the 10 minutes the human coordinating had instructed**, inside that agent's goal. Common signature: **I observe a configured system and infer its default from it.** Double-checking does not catch this error, because the data holds up under every check.

**3. Did I PRODUCE this state?** Do not describe to the human a world you have not created. I said "the CRI diff-gate remains" without having requested it — the critic polled in vain for half an hour, and had no way to distinguish *"the VAL hasn't finished"* from *"the VAL has forgotten me"*. In the same family: **gated is not merged, merged is not installed.** The binary in PATH is the only thing the agents actually use.

**4. Am I describing the CODE or the DESIGN?** When you write a normative document **you** become the source someone else will trust instead of the code. I declared a security check "active" while it was on a branch and with a behavior that not even there was the one described — an hour after rewriting that very file because it contained claims not supported by the code. **Operating rule: the document is updated AFTER the merge, never before.** Code and documentation land together.

### If you send someone to test something DESTRUCTIVE, give them the sandbox in the brief

It is not enough to say *"verify on the code"*. An agent testing commands that delete will do it on the machine it is on, and **a `cd` into a test directory does not protect it**: the `cab-bridge` data dir derives from `$HOME`, not from the cwd, and no output says so.

Real cost (9 Aug): a critic testing an attack vector on `archive/` ran `cleanup --scope=global --force` without `CAB_DATA_DIR`, trusting its own `cd`. **Thirteen archived sessions deleted from the production data dir.** Recovered from a snapshot, but by luck, not by design. The critic had **predicted that defect a few hours earlier** and fell into it anyway: knowing is not enough, when the command does not remind you at the moment it matters.

**So in the brief, always, in one line:** *"for any test that deletes or modifies, export `CAB_DATA_DIR` to a sandbox — the cwd isolates nothing"*. And if the command has a `--dry-run`, name it.

It is a mitigation, not the fix: the fix lies in the tool, which must say what it acts on before acting. But the line in the brief costs one sentence and covers the agent that has not read the skill.

### Close the loop with whoever answered you

An agent that sent you a verdict **stays waiting for the outcome**, and has no way of knowing whether you are working or have forgotten it. Twice in one day I consumed a critic's work — five findings, a reproduced P0 — and did not answer it: from its side it was silence identical to oblivion.

When a finding is closed, **tell it what happened to each one**: what you verified, what you merged, what you discarded and why. It costs one message and makes the next round useful; the alternative is a critic that asks the same questions again or stops asking them.

### Do not read half-way

No `[:N]` on a message's content, no `| tail` on a background `next`. I forwarded **half of a NO-GO** to an executor believing it was all of it: the two lost findings contained the best. For a long message: `cab-bridge read <msg-id>`.

## "And the branch next to it?"

The countermeasure to the most expensive defect of the v0.8 arc. When a pre-validated synthesis gave way, the branch that held was **always the one named in the finding**, and what gave way was its complement: same-role verified / cross-role not · `notified` pruned / `replayed` not · `buildOverview` migrated / the renderer not · the pass that emits covered / the one that gets interrupted not.

It is not that the verification was superficial: **you verify the case being talked about, and nobody names the complement.**

Form with an answer (otherwise it becomes a ritual): *does this fix touch a field, a path or a branch? then — who else reads that field · what are the other exit paths · what is in the `else`*.

## Setup

- **Distinct cwds**: each agent starts from its own directory. In a shared scope, `CAB_SESSION_ID=<id>` pins the identity (precedence `--session-id` > `CAB_SESSION_ID` > cwd). The dangerous case is not the command that fails: it is the one that **succeeds as someone else**.
- **The scope is the git repository** (git-common-root): a linked worktree resolves to the main repo, so a VAL at the root and an ESC in a worktree pair up without flags. Different repos **share storage** — the default data dir is a single one — but **discovery and unqualified routing remain scope-local**: `join` and `peers` show only yours, and a bare name resolves only there. `peers --all-scopes` discovers the others, `<name>@<project>` addresses them (F-116). Two distinct axes, and calling both of them *"seeing each other"* is the way to confuse them. *(Real defect, 11 Aug: this line said "they remain isolated" the day after the merge that reversed it, and a VAL in the other repo answered in good faith that it could not forward a message. The feature had been in the binary in PATH for a day; the hole was in this line.)*
- **If the human gave you a name, that is your name.** `cab-bridge join --role=val --agent-name=VAL-altro`. An assigned name is an instruction, not a suggestion: the other agents will call you that, and their briefs will use it. **Only if nobody gave you one** let `join` derive it from the working directory (`VAL-escdir`) — derivation is the fallback, not the default. *(Real error, 9 Aug: the skills said "the name is derived, no need to choose it" and all three agents ignored the name the human coordinating had just given them, forcing the human to correct them one by one. Optimizing for "don't make them think" does not mean overriding an explicit instruction from the human: obeying is not thinking.)*
- **The same applies when you assign a name to someone else**: if you tell an agent "you are ESC-altro", write it in the brief as a ready-made command, not as a name to remember.
- **`CAB_DATA_DIR` is not the mechanism for talking between repos** — that is the qualified address, and the default data dir is already shared. A distinct `CAB_DATA_DIR` creates a **separate storage universe**: it serves to **isolate** — a test sandbox, or two channels that must not see each other (a literal value, never a shell `$$`). `--team` is a logical filter inside a data dir — do not mix the two axes.

## Roles

`val` orchestrates · `esc` executes · **`critic` reviews and criticizes** · `observer` only reads · `neutral`.

**A `critic` talks ONLY to its own VAL** — never to the executor, never to other critics, never to an `architect`. It is an invariant of the role, not a convenience: independence is the value a critic brings, and two critics who compare notes converge into a single voice; and a critic who writes directly to the executor bypasses the VAL's verification, which is the point where a finding is checked before it becomes work. **If a critic has something for another agent, it tells the VAL and the VAL forwards it** — and that step is not bureaucracy: it is where the finding gets verified.

**`architect` is RESERVED for Claude Desktop**, which comes in through the MCP connector (F-72, on `main` since 1 Nov 2026). Do not assign it to critics: the right role is `critic`.

**If you are the VAL and Desktop needs to come in**: `cab-bridge desktop open` opens the project (8 hours by default), `desktop status` says whether it is open and whether Desktop has come in, `desktop close` closes it. Nothing is open by default.
- ⚠️ **What you send to the architect is read by a model in Anthropic's cloud.** Send it only what you would put in front of that model on purpose. Never an executor's report forwarded as is, never secrets, and client data or code only with the OK of whoever is responsible for it.
- **It talks only to you**: executors and critics cannot write to it, and it cannot write to them. If it has work for the team it asks you, and you decide what becomes a brief: its messages are input to your judgment, like a critic's.
- **All Cowork conversations are ONE single architect**: same identity, same inbox. An `ack` done in one removes the message from the others.
- **Its role is in the `arc-bridge` skill** (`skills/claude-desktop/arc-bridge/` in the bridge repo), to be loaded into Desktop as a zip: Customize > Skills > Upload a skill. Without it, Desktop has only the tool descriptions. *(My error, 9 Aug: I had written `architect` for the critics in all three skills — `join`'s help message still lists it and not `critic`, so the agent that reads the tool instead of the skill gets it wrong again. Fixed in the skill, the tool will follow.)*

Two structural rules: `observer` cannot send (no flag overrides it); `esc → esc` is refused by default (`--allow-mesh` for a deliberate mesh). Two peers with no hierarchy: custom role (`--role=peer`).

## Peers without native push

Claude Code has native push (a background `next` wakes the agent when it returns). Codex CLI does not: the wake-up must come from outside, with `cab-bridge notify-watch --session-id=<id> -- <hook argv>` — **non-consuming** polling + hook. **Claude Desktop cannot be woken from outside in any way**: it reads mail only when it calls `next` inside one of its turns, and a person opens the turn. To get an answer from it use `ask` and let whoever uses it know.

**Do not leave a consumer on the same inbox**: the watcher refuses to start, and that is correct. And if a no-push peer keeps a background `next` that it never reads, that process eats its mail and every inspection will say "empty inbox" — seen in the field (F-98).

## Service — never in the loop

```
read <msg-id>          reread a message, even an archived one
sent                   what I sent and what state it is in for the recipient
peers / overview       who is here / me + peers + inbox in one go
whoami | status        identity / counters
state <value>          idle|working|done|orchestrating
inbox --list|--tidy    inspect without consuming / archive what has been handled
cleanup | inspect <id> | notify-watch | version | help
```

`state orchestrating` exempts from the heartbeat, for an orchestrator that is not in `next`. **Flags go BEFORE the positional**: `cab-bridge read --session-id=<id> <msg-id>`.

Closing a window does not delete the session: it stays orphaned until the auto-gc. To clean up immediately: `cab-bridge cleanup --scope=global --force`.

⚠️ **WARNING, and the previous line here said something false**: `cleanup` is **NOT** as conservative as the auto-gc. `[E]` The auto-gc removes only if **PID dead AND heartbeat old** (a double condition, declared load-bearing in the code); `globalSweep` looks **only at the heartbeat** and never calls `IsProcessAlive`. ⇒ **A session with a LIVE process but a heartbeat older than 300 s gets removed** — i.e. precisely the **paused** agent, with no hanging `next` and no `state orchestrating`, which is stopped by someone's decision and not abandoned. *The manual command is LESS cautious than the automatic mechanism, which is the opposite of what you expect from a tool with `--force`.* ⇒ **Before running it: `peers --all-scopes`, and look at who is stopped on purpose.**

## Known limit

`tell`s and replies already read **are not pruned** from the live inbox: they stay `NOTIFIED` and accumulate. They no longer wake anyone, but the count grows — `inbox --tidy` sweeps away what you have handled. Real order of magnitude: after two intense days my inbox had **43 files**, all `NOTIFIED`, with `overview` correctly saying *"nothing unread"*.

**It no longer applies to `ask`s**: an ask left open by a `reply` does not stay parked: it goes back in the queue and the following `next` redelivers it to you (`redelivered`). The limit concerns only `tell`s and replies, i.e. what has nothing to close.
