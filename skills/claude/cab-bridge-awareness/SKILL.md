---
name: cab-bridge-awareness
description: "Operational use of cab-bridge for the VAL role (orchestrator) and for whoever coordinates multiple agents: the five-command loop (join/next/ask/tell/reply), the mailbox model where next never consumes and only reply archives, the single sending method, the authority rule, structural re-arming with CAB_SESSION_ID, and ground-truth verification. Use when you coordinate two or more agent sessions on the same machine."
metadata:
  author: alan-curtis
---

# Operational cab-bridge

The binary is on `$PATH`; sessions live in `~/.claude/cli-agents-bridge/layout-2/sessions/<id>/`. This file is the core: every rule with one line of reason. The rest is opened when needed:

| when | file | sections |
|---|---|---|
| you engage or wake a Codex peer, or wonder how much it costs to keep it listening | `references/codex-peer.md` | "Engagement", "The cost of a polling Codex", "WAKING A CODEX PEER" |
| you orchestrate: vendor map, agents in other repositories, Claude Desktop, gate under load, `cleanup`, inbox limits | `references/val.md` | "Vendor map", "Agents in other repositories", "Claude Desktop (architect role)", "Gate under load", "Cleanup", "Known inbox limit" |
| you want the reason behind a rule: the incidents, with the date | `references/lessons.md` | e.g. "Authority", "Ground truth", "Destructive tests", "F-135 and merged is not installed" |

## Entry: two commands, and that's it

    1)  cab-bridge join --role=val --agent-name=<the name the human gave you>
    2)  CAB_SESSION_ID=<the id printed at step 1> cab-bridge next     (in background)

Step 1 prints who you are and who is here: do not re-check it with `overview`, `peers` or `inbox --list`, which give the same answer and cost turns. `join --role=val` already puts you in state `orchestrating`, exempt from the heartbeat: between one message and the next you are not in `next`, and nobody gives you up for lost.

If the human gave you a name, that is your name: an assigned name is an instruction, the others will call you that. Only if nobody gave you one, let `join` derive it from the directory. When you assign a name to another agent, write it in the brief as a ready-made command (`cab-bridge join --role=esc --agent-name=ESC-bridge`), not as a name to remember.

Engaging a Codex peer? The first thing you ask it for is its `CODEX_THREAD_ID`: it stays still, costs nothing, and you wake it when there is material (command in the section "Peers without native push"). A Codex with no id and no wake-up that does `join` + `next` and stops believes it is listening and is not; one that polls costs about one turn per minute. Details in `references/codex-peer.md`, sections "Engagement" and "The cost of a polling Codex".

## The loop: five commands, zero flags

```bash
cab-bridge join --role=val          # once, at the start
cab-bridge next                     # then forever: the only command in the loop
cab-bridge ask <agent> < msg.md     # I ask: stays open until they answer
cab-bridge tell <agent> < msg.md    # I inform: does not wait for an answer
cab-bridge reply < msg.md           # I answer whoever asked; closes THAT delivery
```

The verb carries the type: no `--type`, no `--in-reply-to`, no id to transcribe. Recipients are agent names, the ones `join` shows you (only your repository); `reply` finds on its own what it is answering. An agent in another repository is reached with `<name>@<project>`, and only from `val` to `val`: `references/val.md`, section "Agents in other repositories".

## Mental model

| State | Meaning |
|---|---|
| `UNREAD` | arrived, never shown |
| `NOTIFIED` | `next` showed it to you: it is still in your inbox |
| `REQUEUED` | it had been shown to you, you did not close it: it is coming back to you |
| `ARCHIVED` | closed, moved to `processed/` |

- **`next` never moves a file.** Waking up and consuming are separate acts: no process can eat an agent's mail.
- **Only `reply` archives, and it archives one delivery**: the asks that sender sent you in that page of `next`. What arrived later is named under your reply and goes back in the queue (`redelivered`). Read those lines: it is mail you had not seen yet. The reason: `NOTIFIED` means "printed", not "read", and a counter-order that arrived while you were writing must not end up answered.
- **`next` prints two records**: the page (`emitted`) and the outcome (`committed` or `not-committed`). Work only on `committed`, and do not forward an unconfirmed page to anyone.
- `next` has no window: it waits until something arrives; if it is interrupted it says so (`"status":"interrupted"`) with your open `outbound` items.
- From the terminal there are no read ACKs: to know whether a brief has arrived look at the `outbound` line of `next`, or at `cab-bridge sent`. (`ack` exists only in Claude Desktop's MCP connector.)
- After a compact rerun `join`: it redelivers your asks that are still open, marked `redelivered`.

## Single sending method: `Write`, then redirect the path

    1) Write  -> msg.md                       (the content does not touch the shell)
    2) cab-bridge ask <agent> < msg.md

The shell interprets backticks, `$` and quotes before the binary starts: the message leaves in pieces with exit 0, and no tool-side check can notice. With `Write` the text travels as a parameter of a tool and the shell sees only a path. Fallback inside bash: heredoc with a quoted delimiter (`<<'EOF'`); `<<EOF` without quotes interprets everything. Never the text as an argument, not even for two words. It applies to every command that takes text, commits included: `git commit -F <file>`. If you receive a message with holes where file names or commands should be, ask for it to be resent instead of guessing.

## Authority rule: the VAL dismisses nobody

Only the human closes a session. "They have finished the task I gave them" does not mean "they have finished": the human is the one who knows the complete plan. Never "you can switch them off" or "we are done"; say instead: *"CRI has no task; if you have something else I can engage it on X, otherwise it stays available."* An agent at rest costs nothing, turning it back on costs context. If you do not know what the next steps are, ask. It also applies to your own listening: do not stop because everything seems closed.

## Re-arm before reading, in the same call

The orchestrator's most frequent defect is forgetting to start listening again. ESC and the critics have a closed loop (receive, work, reply, re-arm); the VAL, between one message and the next, reads, decides, writes docs, commits, and re-arming becomes one of the N things. It is the structure of the role, not attention.

> When `next` terminates, the first command is the re-arm, in the same tool-call block as reading the message. If you find yourself not listening, re-arm before any diagnosis: the diagnosis does not expire, incoming messages do.

**Always pin `CAB_SESSION_ID`.** The shell's cwd persists, and the VAL enters other agents' directories as part of the job (a worktree's `git log`): from there a command without an id resolves the session of whoever lives in that directory, and succeeds, with no errors and no warnings. It applies to every command (`ask`, `tell`, `reply`, `state`, `read`), and for `next`, which accepts no flags, it is the only way. Precedence: `--session-id` > `CAB_SESSION_ID` > cwd.

    CAB_SESSION_ID=<your-id> cab-bridge next

## Do not read half-way

No character limit on a message's content, no `| tail` or buffering pipe on a background `next` (`tail` emits nothing until EOF). For a long message: `cab-bridge read <msg-id>`, which prints everything.

## Verify the ground truth, not the account

An account (yours, an agent's or a tool's) is not the state of the system: before declaring something look at the disk (`ls` on the inbox, `git log`, the file). It applies above all to whoever holds the gate. A claim about the code inside a plan is verified on the code before ratifying it. And the flip side: a red that disappears on the second attempt does not say "environment", it says "non-deterministic", which is what a race looks like. The log tells them apart: a process that was making progress and was killed by the limit is contention; one that is dead or hanging without making progress is a defect.

**The four questions, before writing a claim** (long form in `references/lessons.md`, section "The four questions, in the long form"):
1. **Did I execute it or deduce it?** Always say so. A plausible argument is not a verification.
2. **Which object is this data a property of?** A true number can concern the wrong thing: the process instead of the model, a version instead of the runtime, a configured system instead of the default.
3. **Did I produce this state?** Do not describe a world you did not create: verified is not merged, merged is not installed, and only `cab-bridge version` counts.
4. **Am I describing the code or the design?** A normative document is updated after the merge, never before.

**"And the branch next to it?"** When a verification holds, what was verified is the case being talked about; nobody names the complement. If a fix touches a field, a path or a branch: who else reads that field, what are the other exit paths, what is in the `else`.

**In the brief of whoever has to test something destructive**, always: *"for any test that deletes or modifies, export `CAB_DATA_DIR` to a sandbox: the cwd isolates nothing"* (the data dir derives from `$HOME`). If the command has a `--dry-run`, name it.

**Close the loop**: tell whoever sent you a verdict what happened to each finding. From their side, the silence of someone who is processing and that of someone who has forgotten are identical.

## Setup and roles

- **The scope is the git repository**: a worktree resolves to the main repo, so a VAL at the root and an ESC in a worktree pair up without flags. `join` and `peers` show only your repository; `peers --all-scopes` shows the others.
- **`CAB_DATA_DIR` creates a separate storage universe**: it serves only to isolate (a sandbox, two channels that must not see each other), never to talk between repositories. `--team` is a filter inside a data dir: do not mix the two axes.
- **Roles**: `val` orchestrates, `esc` executes, `critic` reviews and criticizes, `observer` only reads, `architect` is reserved for Claude Desktop (MCP connector; `references/val.md`, section "Claude Desktop (architect role)"). Do not give critics `architect`: the right role is `critic`.
- **A `critic` talks only to its own VAL**: never to the executor, to other critics or to the architect. Independence is the critic's value, and the passage through the VAL is where a finding is verified before it becomes work. If a critic has something for another agent, it tells the VAL and the VAL forwards it.
- `observer` cannot send; `esc -> esc` is refused unless `--allow-mesh`. Two peers with no hierarchy: custom role (`--role=peer`).

## Peers without native push

Claude Code wakes up on its own with the background `next`. Codex does not: you wake it, with the thread id it gave you:

    codex queue --thread <CODEX_THREAD_ID> --message "you have mail on the bridge"

(`--thread` and `--message` are mandatory; the text is fixed and short, so the rule "never the text as an argument" does not bite here: the real content is on the bridge.) `codex queue` exists from 0.154.0 on: before relying on it, check `codex --version` **of the binary your PATH finds** (`which -a codex` if there is more than one); an older one answers `unexpected argument '--thread'` and the peer stays asleep. Measured on 9 Nov 2026: a machine with two installs, one of them 0.136.0, gave that error to a probe that had found the old one. Details and limits in `references/codex-peer.md`, sections "WAKING A CODEX PEER" and "What is NOT solved yet". Claude Desktop cannot be woken from outside in any way: it reads mail only when it calls `next` inside one of its turns, and a person opens the turn; use `ask` and tell the human.

Do not leave on a peer's inbox a background `next` that nobody reads: the message stays in the inbox marked `NOTIFIED`, `overview` says "nothing unread" and no `next` redelivers it.

## Service: never in the loop

```
read <msg-id>          reread a message, even an archived one
sent                   what I sent and what state it is in for the recipient
peers / overview       who is here / me + peers + inbox in one go
whoami | status        identity / counters
state <value>          idle|working|done|orchestrating
inbox --list|--tidy    inspect without consuming / archive what has been handled
inspect <id> | desktop open|status|close | notify-watch | version | help
```

Flags go before the positional: `cab-bridge read --session-id=<id> <msg-id>`. **`cleanup` can remove a live agent that is paused, and with no flag it deletes the session of the current folder**: do not run it in any form without the human's OK (`references/val.md`, section "Cleanup").
