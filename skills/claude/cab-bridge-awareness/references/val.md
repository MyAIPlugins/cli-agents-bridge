# Orchestration: vendors, other repositories, Claude Desktop, maintenance

Reference of `cab-bridge-awareness`, for the VAL.


## Vendor map (measured from 9 August onward)

**The vendor map, measured on the same bridge from 9-10 August onward, with the date of the latest measurements in the row** (latency = from the file on disk to the listener re-arming, i.e. **the model**, not the process):

    Claude Code    native wake-up            run_in_background, zero cost on empty waits
    Codex          🟢 EXTERNAL WAKE-UP       `codex queue --thread <id> --message "..."`: PROVEN 12 Sep,
                   (since 0.154.0)           with the human control: agent STILL, zero cost while it waits
                   ⚠️ but a PERSON wakes     if instead it POLLS: ~60 turns/hour (ONE PER MINUTE) at ~2,150
                   it: nobody has yet        tokens each ⇒ ~130k tokens/hour of silence
                   connected a watcher
                   to `codex queue`
    Antigravity    native wake-up            3.1 s · zero cost · no goal, no waiting
                   (Reactive Wakeup on background task completion)
    Claude Desktop no shell                  comes in through the MCP connector (F-72, on main since 1 Nov 2026)
                   (Cowork: agent in the     after the VAL's `desktop open`; reads mail when
                   cloud, 60 s per tool)     it calls `next`, i.e. inside one of its own turns


## Agents in other repositories


**For an agent in ANOTHER repository** (F-116, v0.8.0): `<name>@<project>`, where `<project>` is the `SCOPE` column of `peers --all-scopes`: **copy it as printed, and do not add quotes of your own**:

    cab-bridge tell VAL-other@other-project < /tmp/msg.md

**The previous rule said "always quote it", and it was insufficient.** It covers the space and fails on the apostrophe: a repository called `O'Brien's Tools` closes the quote you opened, and the rest of the line goes back to being shell syntax (`zsh: unmatched '`). It is a directory name someone may really have.

**Now the tool does it**: since v0.9.0 `peers` renders the `SCOPE` column already safe as a single argument (**only where needed**, so the ordinary case is identical to before) and `peers --json` stays raw for whoever parses it. If you add your own quotes on top of a value already rendered, you **break** it.

*The principle, which holds beyond this field*: **a quoting algorithm is not put in the reader's hands.** No rule applied by hand holds up in every case: we wrote one that seemed complete, and an apostrophe took it apart.

**Cross-project is `val` -> `val`, BOTH sides** (`verbs.go:283`): it is not enough for the writer to be a `val`, the receiver must be one too. So a `critic` that has something for the other repo passes it to its own VAL, and it is the VAL who talks to the VAL over there.

**The reply branch is deliberately exempt**: if a cross-project ask has reached you, a **bare** `reply` goes back on its own: it infers the sender, it needs no address. It is a one-shot capability on that ask, not a channel that stays open: to write to them **yourself** first later on you need the qualified token again, and `next` delivers **two** to you: **`fromAddressShellArg` is the one to paste into a command**, already safe as a single argument; `fromAddress` is the same raw value, for comparing or parsing. **Do not add quotes to either**: if the project contains an apostrophe, the quotes you add break, and on the first one they are already there anyway.


## Scope, storage, names

- **Distinct cwds**: each agent starts from its own directory. In a shared scope, `CAB_SESSION_ID=<id>` pins the identity (precedence `--session-id` > `CAB_SESSION_ID` > cwd). The dangerous case is not the command that fails: it is the one that **succeeds as someone else**.
- **The scope is the git repository** (git-common-root): a linked worktree resolves to the main repo, so a VAL at the root and an ESC in a worktree pair up without flags. Different repos **share storage** (the default data dir is a single one) but **discovery and unqualified routing remain scope-local**: `join` and `peers` show only yours, and a bare name resolves only there. `peers --all-scopes` discovers the others, `<name>@<project>` addresses them (F-116). Two distinct axes, and calling both of them *"seeing each other"* is the way to confuse them. *(Real defect, 11 Aug: this line said "they remain isolated" the day after the merge that reversed it, and a VAL in the other repo answered in good faith that it could not forward a message. The feature had been in the binary in PATH for a day; the hole was in this line.)*
- **If the human gave you a name, that is your name.** `cab-bridge join --role=val --agent-name=VAL-other`. An assigned name is an instruction, not a suggestion: the other agents will call you that, and their briefs will use it. **Only if nobody gave you one** let `join` derive it from the working directory (`VAL-escdir`): derivation is the fallback, not the default. *(Real error, 9 Aug: the skills said "the name is derived, no need to choose it" and all three agents ignored the name the human had just given them, forcing the human to correct them one by one. Optimizing for "don't make them think" does not mean overriding an explicit instruction from the human: obeying is not thinking.)*
- **The same applies when you assign a name to someone else**: if you tell an agent "you are ESC-other", write it in the brief as a ready-made command, not as a name to remember.
- **`CAB_DATA_DIR` is not the mechanism for talking between repos**: that is the qualified address, and the default data dir is already shared. A distinct `CAB_DATA_DIR` creates a **separate storage universe**: it serves to **isolate** (a test sandbox, or two channels that must not see each other; a literal value, never a shell `$$`). `--team` is a logical filter inside a data dir: do not mix the two axes.


## Claude Desktop (architect role)

**`architect` is RESERVED for Claude Desktop**, which comes in through the MCP connector (F-72, on `main` since 1 Nov 2026). Do not assign it to critics: the right role is `critic`.

**If you are the VAL and Desktop needs to come in**: `cab-bridge desktop open` opens the project (8 hours by default), `desktop status` says whether it is open and whether Desktop has come in, `desktop close` closes it. Nothing is open by default.
- ⚠️ **What you send to the architect is read by a model in Anthropic's cloud.** Send it only what you would put in front of that model on purpose. Never an executor's report forwarded as is, never secrets, and client data or code only with the OK of whoever is responsible for it.
- **It talks only to you**: executors and critics cannot write to it, and it cannot write to them. If it has work for the team it asks you, and you decide what becomes a brief: its messages are input to your judgment, like a critic's.
- **All Cowork conversations are ONE single architect**: same identity, same inbox. An `ack` done in one removes the message from the others.
- **Its skill** (8 Nov): `skills/claude-desktop/arc-bridge/` in the bridge repository, loaded into Desktop through Customize > Skills.


## Gate under load (environment notes of 10 August)

**An environment note that makes the gate less valid, not just slower** (10 Aug): the machine has **8 logical cores**, and three agents compiling together push the load above 10: each sizes its own workers on the number of cores, so three builds are ~24 slots on 8. The mechanism, however, **is not** what it seems, and I had written the wrong version here: TSan does **not** detect races by observing two accesses that collide in time; it builds a **happens-before** relation and flags two accesses that are not ordered with respect to each other even seconds apart. Contention does not hide a race in that sense. What it really does: *(1)* tests with a **timeout** do not get to complete, so **the code that contained the race is not executed**, and what is not executed is not analyzed; *(2)* time-dependent tests become flaky, i.e. spurious reds that cost more than the gate. **The conclusion holds (a gate that runs alone is a better gate) but for the second reason, not the first.**

So: if a red gate happens while others are building, **rerun it alone before believing it**. And the right lever is **`-p`, not `GOMAXPROCS`**: `-p` limits how many *test binaries* run together (separate processes, each with its full GOMAXPROCS, **same internal concurrency**), while lowering `GOMAXPROCS` really does take interleaving away from the exploration. The default of `-p` is GOMAXPROCS, so **a single gate already launches eight binaries on eight cores**: it does not take three of you for a gate to assume it owns the machine.

**Order of magnitude, measured twice** (ESC and I independently): the full gate is **~10.8 s wall-clock, ~9 s of CPU**. The VAL had written "~40 s" from memory: off by a factor of four, and on that number it had built the idea of being the dominant consumer. It is not: what saturates the machine are the **builds** (`next`/turbopack/`tsc`), which last **minutes**.


## Cleanup

Closing a window does not delete the session: it stays orphaned until the auto-gc. To clean up immediately there is `cab-bridge cleanup --scope=global --force --data-dir=<the data dir the command prints>` (without `--data-dir` it refuses, and it stops at the sessions of the current project; `--all-scopes` takes them all; the archive purge instead spans every project in the data dir, and the command prints so), but **you do not run `cleanup` in any form without the human's OK**: even with no flag (scope `my-session`) it deletes without confirmation the session of the current folder, the VAL's included (measured on 9 Nov 2026 in a sandbox). A firm rule, because of the facts below and in `lessons.md` (section "Destructive tests": thirteen sessions deleted on 9 Aug).

⚠️ **WARNING, and the previous line here said something false**: `cleanup` is **NOT** as conservative as the auto-gc. `[E]` The auto-gc removes only if **PID dead AND heartbeat old** (a double condition, declared load-bearing in the code); `globalSweep` looks **only at the heartbeat** and never calls `IsProcessAlive`. ⇒ **A session with a LIVE process but a heartbeat older than 300 s gets removed**, i.e. precisely the **paused** agent, with no hanging `next` and no `state orchestrating`, which is stopped by someone's decision and not abandoned. *The manual command is LESS cautious than the automatic mechanism, which is the opposite of what you expect from a tool with `--force`.* ⇒ **Before running it: `peers --all-scopes`, and look at who is stopped on purpose.**


## Known inbox limit

`tell`s and replies already read **are not pruned** from the live inbox: they stay `NOTIFIED` and accumulate. They no longer wake anyone, but the count grows: `inbox --tidy` sweeps away what you have handled. Real order of magnitude: after two intense days my inbox had **43 files**, all `NOTIFIED`, with `overview` correctly saying *"nothing unread"*.

**It no longer applies to `ask`s**: an ask left open by a `reply` does not stay parked: it goes back in the queue and the following `next` redelivers it to you (`redelivered`). The limit concerns only `tell`s and replies, i.e. what has nothing to close.
