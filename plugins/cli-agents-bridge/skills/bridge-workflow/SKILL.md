---
name: bridge-workflow
description: How to coordinate two (or more) CLI agent sessions with the cab-bridge binary — the five-command working loop (join, next, ask, tell, reply), the mailbox model with explicit states (UNREAD/NOTIFIED/REQUEUED/ARCHIVED) where next never consumes and reply archives one delivery, putting anything that arrived later back in the queue, recipients by agent name, scope = git repository so same-repo worktrees pair automatically, agent state, inbox inspection, and external wake for peers without native push (notify-watch). Use when one agent session needs to hand work to, or exchange messages with, another agent session on the same machine.
---

# cab-bridge — coordinating agent sessions

`cab-bridge` is a single Go binary (on `$PATH` when this plugin is installed) that lets CLI agent sessions on the same machine exchange messages through files under `~/.claude/cli-agents-bridge/sessions/<id>/`. No network, no API calls: each agent reads its own mail and replies from its own live context.

A common shape is **one orchestrator + one executor** (roles `val` and `esc`), but nothing here depends on those names.

## Getting in: two commands, then stop

```bash
cab-bridge join --role=<val|esc|critic> --agent-name=<the name you were given>
CAB_SESSION_ID=<the id printed above> cab-bridge next
```

`join` prints **who you are and who is here**. Do not re-derive that with `overview`, `peers` or `inbox --list`: it is the same answer twice, and a fresh agent that double-checks it burns turns before receiving any work. If `join` succeeded, it does not need confirming.

An orchestrator adds `cab-bridge state orchestrating` once, since it does not sit in `next` between messages.

## The working loop — five commands, no flags

```bash
cab-bridge join --role=val      # once, at the start
cab-bridge next                 # then forever: the only command of the cycle
cab-bridge ask <agent> "..."    # ask something — stays open until they reply
cab-bridge tell <agent> "..."   # inform — no reply expected
cab-bridge reply "..."          # answer whoever asked; closes that one delivery
```

**The verb carries the type.** There is no `--type` and no `--in-reply-to`: whether you are asking or informing is something you already know, so it is language, not configuration. Whether a message stays open, gets replayed after a restart, and how long it is kept all follow from the verb you chose.

**No id is ever typed.** Recipients are agent names, which you read in `join`'s output. `reply` finds what it is answering by itself.

## The mental model (read this first)

Your inbox has three states, and only one command moves a file:

| State | Meaning |
|---|---|
| `UNREAD` | arrived, never shown to you |
| `NOTIFIED` | `next` has shown it to you — **still in your inbox** |
| `REQUEUED` | was shown to you, you did not close it, it is on its way to you again |
| `ARCHIVED` | done with — moved to `processed/` |

- **`next` never moves a file, under any circumstance.** It shows you what is `UNREAD`, marks it `NOTIFIED`, and waits. Being woken and consuming are separate acts — that separation is the whole point of the model.
- **Only `reply` archives, and it archives ONE delivery.** Answering someone closes the asks they sent you **in a single `next` page** — not everything of theirs that happens to be open. Anything of theirs that arrived later is left open, named in `reply`'s output, and **put back in the queue so your next `next` hands it to you again** (marked `redelivered`). Confirmation stays a side effect of doing the work, never a ritual — but the work you confirm is the delivery you were shown.
  Why it is a page and not everything: `NOTIFIED` means *the `next` process printed it*, not *you read it*. Rearm before you start working (you should) and a message arriving **while you write** is `NOTIFIED` without you having seen it — under the old rule your answer closed it too, so a *"stop, do not do A"* could be archived as answered by a *"did A as asked"*. Real incident, not a hypothetical.
  **The honest limit**: no read-ACK exists, so this is not impossible-by-construction — the oldest open page can itself be one you never read. What is guaranteed is *at most one delivery per answer*, and *never in silence*: the responder sees what was closed and what stayed open, the sender sees `closes` on the response and `requeued` in `sent`.
- **`next` has no window.** It waits until something arrives, indefinitely. If it is interrupted it says so (`"status": "interrupted"`) instead of exiting silently — so a wrapper can tell "interrupted while waiting" from "nothing happened".
- **After a restart**, re-running `join` replays your still-open asks, and `next` marks them `redelivered` inline on the message. Treat a re-delivery normally: at-least-once delivery with the duplicate made visible, so you never have to *decide* whether something is a duplicate.

There are no delivery receipts. To find out whether a brief landed, `next` already tells you — its summary carries an `outbound` line with your open asks, who they went to, how old they are, and their state on the recipient's side.

## Payload — one rule

**An argument is the message. No argument means stdin, read to EOF.**

```bash
cab-bridge tell VAL-x "short note"
cab-bridge ask ESC-y < brief.md
```

Prefer stdin (or a file) for anything longer than a line. **The shell interprets backticks, `$` and quotes before the binary exists**, so a message pasted inline can arrive mutilated while the command reports success — no tool-side defence is possible. Writing the text to a file first and redirecting it is the only chain where the text meets no interpreter.

## Setup

- **Distinct working directories.** Sessions are resolved by cwd, so each agent starts from its own directory. In a shared scope, `CAB_SESSION_ID=<id>` (read as input, precedence `--session-id` > `CAB_SESSION_ID` > cwd) pins the identity — never a silent fallback. The dangerous case is not the command that fails, it is the one that **succeeds as somebody else**.
- **Scope is the git repository.** Derived from the git common root, so a linked `git worktree` resolves to its main repo: an orchestrator at the repo root and an executor in a worktree of the same repo pair automatically, with no flags. Different repos stay isolated.
- **Manual isolation, special cases only**: peers in *different* repos that must share a channel need the same `CAB_DATA_DIR` (a literal value, never a shell `$$`). `--team=<name>` is a logical filter *within* one data dir — do not mix the two axes.
- **If you were given a name, that is your name — pass it.** A name assigned by whoever started you ("you are CRI2-payload") is an instruction, not a suggestion: the other agents will address you by it, and their briefs will use it.

  ```bash
  cab-bridge join --role=critic --agent-name=CRI2-payload
  ```

  **Only when nobody gave you one** let `join` derive it from your working directory (`ESC-escdir`), which keeps it distinct without a decision. Derivation is the fallback, not the default — obeying an explicit instruction is not "thinking", and skipping it costs the humans a round of corrections.
- If a name is already taken by a live session elsewhere, `join` stops and asks rather than creating a second session with one name — an ambiguity that would break every recipient lookup downstream.

## Roles

`val` (orchestrates), `esc` (executes), **`critic`** (reviews and criticises — **sends only to its `val`**, which passes things on: independence is the point of the role, and two critics comparing notes converge into one voice), `observer` (reads only), `neutral`. `architect` is **reserved** for Claude Desktop arriving over the MCP connector; do not assign it to reviewers. Custom roles are accepted by routing. Two structural rules:

- **`observer` cannot send.** No flag overrides it — it is read-only by design.
- **`esc → esc` is rejected** by default; route through the orchestrator, or pass `--allow-mesh` for a deliberate mesh. Two equal agents with no hierarchy should use a custom role (`--role=peer`), which is allowed out of the box.
- **A `critic` speaks only to its own `val`** — never to the executor, never to another critic, never to an architect. This is the role, not etiquette: independence is what a critic is for, and two critics comparing notes converge into one voice; writing straight to the executor would bypass the orchestrator's verification, which is where a finding gets checked before it becomes work. A critic with something for someone else tells the `val`, and the `val` relays it.

If you are a reviewer, `critic` is the role meant for you.

## Peers without native push

Claude Code sessions have native push: a backgrounded `next` wakes the agent when it returns. **Codex CLI does not** — measured: the process picks the mail up in milliseconds, but the model only sees it when it gets another turn.

Such a peer stays reachable by **holding a persistent goal in its own runtime** — the goal is what grants it the next turn; without one it finishes a reply and ceases to exist until a human writes to it. Its goal must say **wait on the `next` process itself, asking for the longest window its wait tool accepts, and never `sleep` alongside it**, plus *one consumer at a time*. `next` has no window: it hangs until mail arrives, so the cycle is holding the wait open — the window is how long you wait, not a pause between rounds. A long window is free **because `next` delivers and exits**, so the wait returns on process exit: measured on Codex, the wait does *not* return on intermediate output (output at 2 s on a process that lived 30 more returned at 31.9 s), so do not carry this rule over to processes that keep running. Shortening the window buys nothing and costs a model turn each time. Three regimes, all measured: **no wait at all (goal only) ~450 turns/hour** — one turn every 8 seconds to say "nothing", which nearly exhausted a weekly quota in minutes; **5-minute slices, 12/hour**; **1-hour slices, ~1/hour**. On Codex the cap is `background_terminal_max_timeout` (default 300,000 ms, raisable to 3,600,000, loaded at startup). Beware silent truncation: asking beyond the cap **returns no error** — 3,600,000 requested came back empty at 300,036 ms — so if the number matters, measure the return instead of trusting what you passed, and put "the longest window your tool accepts" in the goal rather than a number that will age. Two opposite failures on one day: a peer that did not wait at all re-polled every 10-30 seconds, and a peer that put `sleep 300` beside its `next` burned a turn per nap while staying blind to a question its own `next` had already delivered. The pattern that holds — verified over 14 unbroken hours — sits between them. The goal must also say that **a delivered message is worked immediately** — obvious until a timing rule sits next to it, at which point the timing rule wins and the message waits. That belongs in the peer's own skill, not in your brief — but if a peer answers once and then goes quiet, **a missing goal is the first thing to check.**

If low latency really matters, the wake has to come from outside instead:

```bash
cab-bridge notify-watch --session-id=<id> -- <hook argv>
```

An external watcher that polls the peer's inbox **without consuming** and runs a hook on a new batch. It refuses to run alongside a consumer on the same inbox, which is correct — do not work around it by leaving both.

## Service commands — never in the loop

```
read <msg-id>          re-read a message, including an archived one
sent                   what I sent and the state it is in for the recipient
peers                  who exists (table or --json)
overview               me, my peers and my inbox at a glance
whoami                 this session's identity
state <value>          declare what I am doing: idle|working|done|orchestrating
status                 own session counters
inbox --list|--tidy    inspect without consuming, or archive what is handled
cleanup                remove own session, or --scope=global
inspect <id>           print a session manifest
notify-watch -- <hook> external wake for a peer with no native push
version | help
```

`state orchestrating` makes a session heartbeat-exempt, for an orchestrator that is not sitting in `next`. Flags go **before** a positional argument: `cab-bridge read --session-id=<id> <msg-id>`.

Closing a window does not delete its session — it lingers until the auto-gc threshold (dead PID + stale heartbeat). To clear dead ones now: `cab-bridge cleanup --scope=global --force`; a live session is never removed.

## Known limitations

- **Already-read `tell`s and responses are never pruned** from a live inbox: they stay `NOTIFIED` and accumulate in a long-lived session. They never wake you again — `next` will not re-emit them — but the count grows. Use `inbox --tidy` to sweep what you have handled.
- **Quoting is on you.** See the payload rule above.

Exit codes: 0 ok, 1 validation, 2 routing-forbidden, 3 cleanup-confirm-required.
