# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.8.0] — 2026-08-08

**Breaking.** The delivery model changes: from **wake coupled to consume** to a **mailbox with explicit states**. Whoever woke an agent used to have already eaten its messages and handed them over on stdout — one defect behind three symptoms reported from daily use: an orchestrator that forgets to re-arm its listener and loses messages, ACKs that agents mistake for content, and commands that hide the other messages that arrived alongside.

Ordering principle for the whole arc: **not making the agent think wins**, ahead of API elegance and orthogonality. Every option on the hot path costs thinking on every cycle, on every agent, forever.

### The LOOP surface — five commands, zero flags

```
cab-bridge join              once, at the start
cab-bridge next              forever: the only command of the cycle
cab-bridge ask <who> "..."   asking — waits for an answer
cab-bridge tell <who> "..."  informing — does not
cab-bridge reply "..."       answering whoever asked — closes it
```

**The verb carries the type.** No `--type`, no `--in-reply-to`, no id to transcribe: whether you are asking or informing is something the writer already knows, so it is language, not configuration. Outbound line, replay on `join` and retention all follow from the verb.

A **SERVICE surface** (`read`, `sent`, `state`, `peers`, `overview`, `cleanup`, `inspect`, `notify-watch`) is *listed*, not merely promised — for an LLM an undocumented command is not absent, it is *to be invented* (LL-13).

### Added
- **`next`** — the one command of the cycle. Delivers what is `UNREAD`, marks it `NOTIFIED`, and **never moves a file under any circumstance**. Wake cursor in a separate schema-versioned file, every read-modify-write under the cross-process session lock. Order is mandatory: **print first, commit the cursor after** — a crash in between duplicates (harmless, at-least-once), the reverse order loses silently. **No window**: no timeout, not even in config — a deadline would be the waiter dismissing itself.
- **`join`** — idempotent entry point; prints **everyone who is here**, never a chosen peer. Stops and asks on a name mismatch instead of creating the second session that blocks every id-free command (F-90). Replays open asks so a compact cannot bury a brief.
- **`ask` / `tell` / `reply`** — recipient **by agent name**, fail-closed: zero matches is an error listing who is here, several live matches is an error listing the candidates. Never a silent pick. Payload: **argument present → it is the message; absent → stdin to EOF**.
- **`reply` is transactional** — it closes *all* the open asks of one sender: frozen set under lock, one journal, deterministic idempotency key, create-if-absent delivery, `SENT` → archive with a progress index → journal removed. A retry completes **without resending**.
- **Outbound and open-ask lines in `next`'s summary** — the compensating half of removing ACKs: the sender sees whether a brief landed without having to remember a command.
- **`architect` in `join --role`** — a real routing role that the entry point did not advertise, so a fresh reviewer picked `esc` and hit the `esc→esc` wall before discovering the role meant for it.
- **`CAB_SESSION_ID` read as input** — precedence `--session-id` > `CAB_SESSION_ID` > cwd, never a silent fallback. The prerequisite for running several agents from folders of one repo: the dangerous case is not the command that fails, it is the one that **succeeds** as somebody else.

### Removed
- **`listen`, `receive`, `bootstrap`, and the ACKs.** Clean break, no compatibility shims, no aliases: there were no active sessions, so this was the moment. ACKs are replaced by queryable state (`sent`, with honest states — an I/O error is an *error*, never a state).

### Fixed
- **`deriveAgentName` was not injective** — it derived from the scope basename, so every agent of the same role in one repository landed on the same name: not an edge case, the default. A second `join --role=esc` resumed the first agent's session, replayed its asks and evicted its `next`. Now the name derives from the working directory, and pair inheritance is gone entirely: once the suffix *is* a directory, inheriting it puts somebody else's directory in your name.
- **F-91** — the shared-scope guardrail warned on an exact match and advised `--session-id`, which the LOOP commands reject: an unexecutable suggestion on every command is a dead end, not noise.
- **F-95** — there was no way to be alive *and* not woken by ACKs: `listen` held the heartbeat but woke on ACKs, `receive --any` ignored them but did not heartbeat. It was not the wrong tool being chosen — there was no right tool. The two commands became one.
- Re-delivery is marked **inline on the message**, not on another command's stderr minutes earlier; an interrupted wait reports `interrupted` instead of exiting 0 with zero bytes; `overview` tells the truth during a genuine wait.

### Known limitations
- **No TTL on the live inbox** (deferred to Tier 2, stated in the design contract): already-read `tell`s and responses stay in `inbox/` as `NOTIFIED` and accumulate in a long-lived session. Slow degradation, not a correctness defect — `next` never re-emits them. A time-based pruner over a live mailbox is a new mechanism deserving its own design round, not a pre-merge patch.
- **Quoting is not solved.** The shell interprets backticks and `$` *before* the binary exists, so no tool-side defence is possible: the command succeeds while the message leaves in pieces. Send from a file. The structural fix is the MCP server (F-72), where content travels as a structured parameter.

### Security — the read surface (found by using it, and by two independent reviewers)

Everything below landed *after* the mailbox rewrite, from a security pass that started as one finding and ended as six crossings of the same boundary.

- **SC-3 is active** (declared since v0.2.0, called by nothing for seven releases). Every read inside the data dir verifies the file belongs to the current uid: `O_NOFOLLOW|O_NONBLOCK`, regular-file only, `fstat` on **the descriptor that is then read** — no window between the check and the use. It also covers **directory entries whose name is interpreted without reading them** (`sent` derived an "archived" state from a filename; `status` counted `.json` names): a planted name could forge either. That second category was missed by the first audit because the audit looked for *file reads*, and there is no read there to find.
- **SC-7 now validates the path, not just the base.** A symlink on `sessions/` redirected the entire data dir while every leaf check still passed — the files behind it are regular files legitimately yours, so the ownership check had nothing to object to. The same shape on `archive/` sent the retention purge to delete **outside** the data dir. The base plus those two directories are now checked with `Lstat` on every command, and on both data dirs when a config file moves you.
- **On a mismatch the policy now belongs to the command, not to whichever helper it called.** Commands that deliver or mutate refuse; commands that list skip the entry and **say so, naming the path** — a diagnostic that dies on the anomaly it exists to show is useless exactly when it matters. Notably `status` no longer reports a silent `0` for an inbox holding a file that is not yours.
- **`cleanup --scope=global` no longer crosses projects** (**breaking**): it removes stale sessions of the project root it is run from, and says how many it left untouched elsewhere; `--all-scopes` restores the previous behaviour. **Scripts relying on the whole-machine sweep must add `--all-scopes`.** The same confinement now applies to the auto-gc that runs on every `join`/`register` — which previously swept other teams' idle sessions with no flag, no confirmation, and nobody having asked for it.
- **`retention_days = 0` disables the purge.** It used to delete the *entire* archive: the value you would reach for to mean "keep everything" was the one that kept nothing. Negative values are disabled too, and the flag and the env var now mean the same thing. Retention itself still spans the whole data dir **by design** — it is a data-minimisation policy, and one that only applied to whoever happened to run `cleanup` would not be a policy — but it now announces what it removed, how many archived sessions were in it, and that it covers every project.

### Roles

- **`critic` is a first-class role** — the reviewer/critic seat. A critic **speaks only to a `val`**: not to the executor, not to another critic, with no override flag. Independence is what a critic is for, two critics comparing notes converge into one voice, and going straight to the executor bypasses the orchestrator's verification.
- **`architect` is reserved** for Claude Desktop arriving over the MCP connector, and is no longer the role reviewers are told to use.
- The role lists were **three, and already diverging** — `join` offered four, `register` five, and the error message described `architect` as the critic. They now come from one source.

### Onboarding

- **`join --agent-name=X` renames in place** instead of stopping: same session id, same mailbox. Correcting a name used to require deleting the session and creating another, which changed the id and left peers writing to nothing.
- **`join` with no name no longer stops on a session that is already yours.** It used to derive a name even when it had nothing to create, compare that invention against the real name, and call the difference a collision — which meant an agent given a name by a human hit the same wall on *every* re-arm, forever.

### Observability

- One source for "am I waiting": the wait marker left the manifest, so an evicted instance can no longer clear the marker a live one just wrote (`overview` reported `not listening` to a session that was listening, which pushed agents into evicting their own healthy waiter).
- `next` records carry the agent name and the sender's open asks, including on the interrupted path.
- `peers`, `overview` and `status` count **unread**, not files — with pure-read delivery a message you have already seen stays in the inbox, so the old count answered a question nobody was asking.
- **The `notify-watch` guardrail was dead** and nothing said so: it depended on a field that v0.8 stopped writing, so the condition was permanently false and the watcher no longer refused to start next to a live consumer. Repaired, with the reason rewritten — the hazard is no longer double *consumption* of files but a double *wake*.

### Method
Four full design-gate rounds with two cross-vendor critics (Codex and Fable) before a line of production code, then per-phase diff-gates. **Six P0s, four of them inside the VAL's own syntheses** — a synthesis that resolves a critic's objection arrives at the next round *pre-validated*, and that patina makes it hard to question, most of all for whoever wrote it. The final acceptance pass found a P0 deeper than the one just closed, on the branch the gate had not named. See `docs/DESIGN-v0.8-mailbox.md` for the normative contract and CLAUDE.md LL-18.

---

## Hardening between the merge and the tag (9–10 August)

v0.8 was merged on the 8th and **not tagged**: the release waited for real use. It paid — roughly fifty fixes landed in the two days that followed, and **almost every one was found by *using* the bridge, not by reading it**, after seven sprints of gates and a public pre-release audit. They are part of this release.

#### Security — the read surface

- **SC-3 is wired.** Declared as a mandatory MVP control since v0.2 and called by nobody, it is now on the live path: every read inside the data dir verifies the owner on the descriptor it then reads (`O_NOFOLLOW|O_NONBLOCK` + `fstat`), and rejects non-regular files. The error no longer disguises itself as "corrupt".
- **SC-7 validates the whole path, not just the base.** A symlink on `sessions/` redirected the entire data dir while *every leaf check passed* — the files behind the link are legitimately ours. Reproduced, then verified closed on both vectors.
- **Six read-surface crossings** closed. The two worst were invisible to a grep for file reads, because they consumed **directory-entry names** rather than file contents: a name is not a proof.

#### Destructive commands must say where

- **BREAKING — `cleanup --scope=global` stops at your own project.** `--all-scopes` reopens the old radius.
- `cleanup` now **declares the absolute data dir before removing anything**, and `--force` cannot suppress that line: an unattended run cannot read a warning, so what stops it is having had to name the target. Prompted by real data loss — an agent purged 13 archived sessions from the production data dir after a `cd` into a sandbox, because the data dir comes from `$HOME` and nothing said so.
- `CAB_RETENTION_DAYS=0` **disables** the purge instead of deleting everything: it was the value an operator would pick to mean "touch nothing".
- The retention purge reports how much it took and from how many projects.

#### Identity and names

- **The naming rule**: rename in place within a directory; **take over a stale namesake** (renamed `<name>-superseded-<id>`, never deleted); **refuse when the previous one is alive**; **refuse across projects**, with an error that says how to free a name held by a dead session.
- **BREAKING — one directory, one workstation.** Re-joining from the same directory with a different role **updates the role on the same session** (`same session, same inbox`) instead of forking a second one. Previously a restart with a changed role produced two live sessions under one name and froze addressing by name until a human ran `cleanup`. The model already assumed it and `findSessionHere` already declared it; now it is enforced.

#### Roles

- **`critic` is a first-class role and talks only to its `val`** — a structural invariant, no flag. `architect` is **reserved** for Claude Desktop over MCP and no longer suggested to reviewers by an error message.
- Every surface that lists roles now comes from one source, and **the bare list is no longer exported**: outside the package there is no way to print it without the reservation. The invariant is held by the compiler, not by discipline.

#### `reply` — what an answer closes

- **The typo guard now catches typos.** It was `strings.EqualFold`, so it caught capitalisation and nothing else, while the comment above it cited a transposition as its motivating example: `echo report | cab-bridge reply VAL-brige` sent **nine bytes of typo** in place of the report, closed the ask and exited 0. Now edit distance, with a threshold that scales with name length.
- A single argument is the message, without exceptions, when there is nothing to disambiguate.
- **BREAKING in effect — an answer closes ONE delivery.** `reply` archives the asks that sender showed you in a **single `next` page**, not everything of theirs that is open. Found in the field: a *"stop, do not do A"* that arrived while the answer was being written was archived as answered by a *"did A as asked"*. The unwritten premise was `NOTIFIED` = *you read it*; it means *the `next` process printed it*, and with rearm-before-work those are different things.
- **What stays open comes back.** Asks left open are put back in the queue and re-delivered by the next `next`, marked `redelivered` — measured at 1.06 s when a waiter is already armed. New sender-visible state **`requeued`**: *shown to them, not closed, on its way again*.
- **Honest limit, stated rather than rounded off**: without a read-ACK — deliberately removed in this release — no rule can know what the agent read. The oldest open page can be one it never saw. What is guaranteed is *at most one delivery per answer*, and *never in silence*.

#### Observability

- `closes` travels in the `next` page, so the sender sees **which** of its asks an answer closed and can recognise one it did not expect.
- `sent` stops asserting a mechanism it cannot prove: `archived` now reads *"out of their inbox: a reply closed it, or they tidied it away (neither proves they read it)"*. The old wording was false by construction for a `tell`, which cannot have a reply.

#### Talking across repositories

Two orchestrators in two projects **could not reach each other**. They saw each other in `peers --all-scopes` and got *"no agent named X in this scope"* when they wrote — the list was wider than reachability and said so nowhere; the boundary appeared only after the attempt, inside the error. Removing ids from the hot path (right, and the point of this release) had taken with them the only way to address another scope, and nobody noticed.

- **Qualified address**: `ask VAL-other@their-project`. The token after `@` is the `SCOPE` column of `peers --all-scopes`, copied exactly as printed — a basename, or the **full path** when two projects share one. That column now prints the repository instead of an absolute path, shortening only where it cannot mislead, decided over the whole list rather than row by row. `inspect` gained the full path, which it never printed.
- **The rule the grammar rests on**: *every token `peers` prints can be pasted into a command.* Break it and the discovery list is wider than reachability again — the very defect this closes, reappearing on the branch meant to fix it. (F-124 later showed the rule was not yet true for the SCOPE half, and closed it in two lots: a grammar for names, then a renderer for paths.)
- **Identity is `(name, scope)`**, fail-closed on duplicates, never a silent pick. `@` is refused in a name somebody **typed** and **sanitised** in one **derived from a directory** — the default name is the project path's basename, so a folder called `foo@bar` would inject the separator with nobody having typed it, and a hard refusal would fail a `join` for a choice the user never made.
- **`reply` never needs an address, not even across repositories**, and now disambiguates homonyms by `(name, scope)`. The scope of that index comes from the **sender's manifest**, not from the message's provenance field: routing is a question about *now*, provenance is a claim about *when it was written*, and messages already in an inbox predate the field.
- **Provenance**: `fromScope` is **additive** — `fromProject` keeps its value and meaning — shown in `next` only when it differs from the reader's, alongside a `fromAddress` already assembled (and, since F-124 lot 2, `fromAddressShellArg` — the SAME value rendered as one POSIX shell argument, which is the only one to paste into a command; `fromAddress` is the logical form, for parsing) so an agent copies instead of building. On legacy messages it is absent and never inferred.
- **Limit of this release, declared**: only `val → val` may **open** a conversation across projects. It binds at the gateway on the manifests actually used to compose the message, not only at name resolution — a role can change in between, and F-110 made role change part of the normal path. **The constraint governs who may start, not who may answer**: `reply` is exempt, and that exemption is a one-shot capability derived from already-open state, migration and compatibility included, now pinned by a regression test rather than left as the absence of a check.

Two design-gate rounds and two diff-gate rounds with a cross-vendor critic produced **ten findings** on this feature alone; **the tenth was in the normative document, written by the VAL, on the one surface nobody had reviewed** — a sentence claiming an open ask "must have passed the checks to exist", which is false for any message delivered by an earlier binary.

## [0.7.0] — 2026-06-05

A single `v0.7.0` tag that also ships the previously-unreleased v0.6 work (the F-39/F-81/F-66 section below, never tagged standalone). Built end-to-end via the cab-bridge dogfooding triad over the bridge itself (VAL + ESC Claude Code + CRI Codex): Tier A by independent VAL-gate; **B-1 and B-2 by the full cross-vendor round** (CRI design-gate → impl → VAL gate → CRI diff-gate → fix → re-gate). The B-1 diff-gate caught 3 P3s, the B-2 diff-gate caught **2 concurrency P1s invisible to the green `-race` gate and the smoke** — without that round B-2 would have shipped a silent brief-loss. Distilled from 7 real chatterence-bi feedbacks.

### Tier A — CLI ergonomics (distilled from 7 real dogfooding feedbacks)

Small, isolated, low-risk fixes distilled from 7 real dogfooding feedbacks (chatterence-bi: ESC + VAL + CRI, ~10h+ across 3 heterogeneous agents). Built on the bridge itself (VAL↔ESC over `cab-bridge`); independent VAL gate `go test -race -count=1 ./...` green (10/10, zero cached) + `go vet` clean + a per-fix real smoke. The dominant feedback finding — the shared-scope id-collision guardrail — is a separate, CRI-gated hardening arc (next).

#### Added
- **A-2 — `ask` accepts `--type=question` as an alias for `query`**: "question" (the natural word) was rejected deep in the write gateway by the strict type enum, silently losing the message. `runAsk` now normalizes `question` (any case) → the canonical `query` — a courtesy of the CLI input layer; the wire schema enum and the shared `sendMessage`/auto-ack paths are untouched. Any other unknown type fails in `runAsk` with an actionable error (the user-facing list `query|response|ping|notify|event` + a "did you mean 'query'?" on a near-miss) BEFORE touching config or the filesystem. New exported `message.IsValidType` so the CLI validates against the SAME enum without duplicating it (DRY, no drift).
- **A-3 — `overview --session-id`** (F-86): `overview` derived "me" from the cwd only, so in a worktree or a shared scope it resolved the wrong session (an ESC worktree seen as the VAL) — useless exactly where it is needed. An explicit `--session-id` now wins (`resolveSessionID`, SC-4 validated); the default stays the id-free cwd lookup (F-42), with a miss message that also points at `--session-id`.
- **A-4 — `ask` echoes `replying_to=<resolved-id>` on stderr** (F-80): with `--in-reply-to=last` the resolved id was opaque, so the sender could not confirm it threaded onto the right message (a confabulation surface, LL-13). The echo is on stderr — stdout stays the bare msg-id so `$(cab-bridge ask ...)` capture is intact; diagnostic like the F-34/F-43 warnings.

#### Fixed
- **A-1 — the F-34 unread-warning now suggests an EXECUTABLE `read` command**: it printed `cab-bridge read <id>`, which in a shared scope resolves the wrong session by cwd lookup and fails with "message not found". It now emits `cab-bridge read --session-id=<sid> <id>` with the sender's own id (the unread message lives in the sender's inbox) and the flag BEFORE the positional (Go flag parsing requires it).
- **A-5 — actionable error when `--session-id` is passed to `register`/`inspect`**: the "always pass `--session-id`" rule (correct for the shared-scope collision) hit a cryptic stdlib "flag provided but not defined: -session-id". Both now define `--session-id` only to reject it with a message teaching the correct form (`register` → `--resume`; `inspect` → the positional `cab-bridge inspect <id>`), right after `Parse`, before any FS access. Happy paths untouched.

### B-1 — shared-scope id-collision guardrail (the #1 feedback finding, CRI-gated)

When VAL@root + ESC/CRI@worktree share a git-repo scope (F-41 pairing), an id-free command resolved "me" by cwd lookup and could silently pick the WRONG session — a silent wrong-session bug (7 independent feedback voices, incl. the cross-vendor CRI). Built via the full cross-vendor round (CRI design-gate → impl → independent VAL gate → CRI diff-gate, which found 3 P3s invisible to the green gate → fix → re-gate → CRI check → merge).

#### Added
- **`resolveCurrentSession` — one guardrailed chokepoint for id-free resolution**: all id-free commands (ask/read/listen/state/inbox/connect/cleanup/sent/status/whoami/notify-watch + receive + overview) now resolve "me" through a single cmd-level helper backed by a new pure `Manager.LookupByCWDDetails`, which in ONE scan surfaces two collision signals:
  - a **HARD ambiguity** (2+ manifests matching the cwd at the same maximum ProjectPath length — `LongestPrefixLookup` silently took the first) is **REJECTED** with an executable `pass --session-id=<id>` listing the contenders;
  - a **shared-scope hazard** (other sessions in the resolved session's non-empty scope with a different ProjectPath) **WARNS on stderr** — naming the resolved session, the siblings, and an executable `--session-id` remediation — or is rejected under opt-in `CAB_BRIDGE_STRICT_SESSION_LOOKUP=1`.
  An explicit `--session-id` **bypasses** the guardrail entirely (a disciplined caller sees nothing). The warning is **stderr-only** — `--json`/`--emit=json`/NDJSON stdout stays valid. By design it does NOT prefer live/non-stale sessions, filter by team, canonicalize ProjectPath via symlinks, or pick by agent/role/`.git` (design-gate constraints).

#### Changed
- **`LongestPrefixLookup` untouched** (Register's collision check still uses it); the new pure lookup lands beside it. The old `resolveSessionID` cwd-lookup branch is **removed** — reduced to `validateExplicitSessionID` (an empty id is now a caller error, not a lookup) so no future id-free caller can re-bypass the guardrail (the dead-branch trap the diff-gate flagged).

### B-2 — orphan-listener reclaim + F3 consume correctness (CRI-gated)

After a `/clear` the previous Claude is gone but its background `listen` can still run as an orphan, holding the session "live": `register --resume` blocked on it (`ErrIdentityLive`), and worse, the orphan `--wait-one` could CONSUME the next brief and wake a dead session (F3 — a silent consumed-but-not-delivered loss). Built via the full cross-vendor round (CRI design-gate → impl → VAL gate → CRI diff-gate, which found **two concurrency P1s invisible to the green `-race` gate AND the `--wait-one` smoke** — a consumed-but-not-delivered race in the streaming path, and a heartbeat TOCTOU manifest clobber → fix → re-gate → CRI re-check → merge).

#### Changed
- **`register --resume` default-reclaims a live identity match** (inverts F-27): a live manifest PID proves only that a `listen` is alive, not that the agent that owned it is. The most-recent identity match (agent-name + role + scope + team) + `--resume` IS the semantic claim to that session's continuity, so it is RECLAIMED — under the session lock it revokes the previous listener then adopts (atomic revoke+adopt). `--force-new` still creates a deliberate second instance; a different identity coexists; two SIMULTANEOUS `--resume` of the same identity is an operator error (lock contention). `ErrIdentityLive` is removed. BUG-6 `ErrSessionExistsForProject` (register one-shot per ProjectPath) is unchanged.

#### Added
- **Listener-ownership fencing (F3 correctness)**: a per-session `listener.json` (SEPARATE from the manifest so a stale heartbeat RMW cannot clobber a revocation) carries a monotone `generation` + a random per-claim `token` — the ownership discriminant, **fail-closed** (missing/parse-error/mismatch all read "not current"). `listen` claims it at startup; `register --resume` revokes it on reclaim. The consume path (`consumeInboxEntry`) re-checks ownership IMMEDIATELY before `MoveToProcessed` (closes the ReadDir→move race) AND the streaming path hands the moved message off with a **blocking send** (closes the move→deliver race) — so a revoked orphan never consumes, and a moved message is always delivered. The fenced heartbeat serializes its manifest RMW with the same session lock and re-checks ownership under it (closes the check→write TOCTOU). `receive --any` is one-shot and stays unfenced. Soft-eviction only — no `kill`.
- **Reclaim observability**: `register --resume` reports the superseded listener (generation/PID); `overview`/`inspect` show `listener.json` (generation, PID, claimed-at, reclaim-pending).

## [0.6.0] — shipped within the v0.7.0 tag (never released standalone)

Built by the cross-vendor TRIAD on the bridge itself (VAL Claude + ESC Claude + CRI Codex over `cab-bridge`): F-39/F-81 = brief → independent VAL gate → merge; **F-66 with the full rigor** — CRI design-gate (naive-loop → serious-watcher) → ESC impl → VAL gate → CRI diff-gate (two P1s the green gate did not see) → ESC fix → VAL re-gate → CRI check → merge. Independent VAL gate `go test -race -count=1 ./...` green + code audit + real smoke per feature.

### Added
- **F-39 — `ask --in-reply-to=last`**: symbolic reference resolving to the id of the most recent non-ack message received from `--to` — so an agent replies without transcribing an opaque msg-id (the LL-13 hallucination surface; the #1 recurring friction, asked by both ESC and CRI). New helper `lastReceivedFrom` (scans `inbox/`+`processed/`, matches on decoded `From`/`Timestamp`, sentinel `ErrNoMessageFromPeer`); explicit `--in-reply-to=<msg-id>` untouched (`last` is intercepted before the `^msg-` format check).
- **F-81 — listener observability in `overview`**: a `listener:` line — `listening (PID, expires in Y)` / `not listening` — plus `--json` `listenerActive`/`listenerPid`/`listenerUntil`. Active iff `IsProcessAlive(PID) && ListenUntil` in the future (the AND guards a stale `ListenUntil` from a dead listen — distinguishes a real listener from the register-then-die heartbeat). New `Manifest.ListenUntil *time.Time` (pointer, not value: `omitempty` does NOT drop a zero `time.Time` struct — only a nil pointer; same reason as `message.InReplyTo`), written by `listen` at startup via `SetListenUntil`.
- **F-66 — `cab-bridge notify-watch`**: external inbox watcher for no-push peers (e.g. Codex CLI) — polls the inbox NON-consumingly (`collectPendingForNotify`, inbox-only, non-ack) and on a new batch runs a configurable hook (`-- argv...`, e.g. `screen -X stuff` to inject into a Codex TUI). Six hardening invariants from the CRI design+diff gates: argv-direct default (`--shell` opt-in; env = metadata only, never content/preview); batch coalescing (one hook per batch, not per message); persistent dedup (marks only after hook exit-0, backoff on failure, prune); `ListenUntil` guardrail + per-`watch-name` lock against a concurrent consumer; structured stderr log + once-per-file skip logging; dirty-only state save (no idle write-storm) + Unix process-group teardown on timeout (`Setpgid` + `kill(-pgid)`, so `screen`/`tmux`/`sh -c '... &'` are not orphaned). The bridge's "second wake door" for vendors without push (LL-16).

## [0.5.1] — 2026-06-01

Backlog-minore follow-up to v0.5.0 — anti-degradation hardening — built same-day via the dogfooding workflow (independent VAL gate `-race -count=1` green + real smoke per fix).

### Added
- **F-49 — `receive --any --unseen`**: wakes only on messages that ARRIVE after launch, ignoring (and leaving in the inbox) the pending already present. Cutoff = now-at-launch (single-machine clock-skew is nil; no opaque timestamp to transcribe — anti-LL-13, unlike a `--since` flag). `--unseen` requires `--any`; plain `--any` is unchanged (`since.IsZero()` keeps the old behaviour).
- **F-37 — `ask --in-reply-to` existence validation**: warns on stderr (and sends anyway) when the referenced id is not found in your `inbox/`/`processed/` — a well-formed but never-existent (hallucinated, LL-13) id no longer passes silently; `--strict-reply` rejects instead. Reuses `findMessage` (F-48); the format is validated first. With F-43 (duplicates) and F-34 (crossings) this completes the LL-13 anti-hallucination safety-net trio.

### Fixed
- `receive --unseen` alone now reports the precise `--unseen requires --any` instead of falling through to the generic `pass --msg-id or --any` (precedence fix).

## [0.5.0] — 2026-06-01

*AI-friendly under stress* (LL-13/LL-14): reduce the surface of opaque ids/artifacts an agent must handle by hand — zero-config onboarding, id-less wake, content/state without digging, and guards against the crossings/duplicates a degraded agent produces. Built end-to-end via the cab-bridge dogfooding workflow (VAL↔ESC over the bridge itself, hands-free via the first-arriver-listens pattern); independent VAL gate `go test -race -count=1 ./...` green + a real smoke per feature; **validated in real use** (real-estate field session, 5 prod deploys: `overview` "the single most useful upgrade", `state` "gold", the bridge "made itself forgotten").

### Added
- **F-40 — `cab-bridge bootstrap --role=<val|esc>`** (zero-config pairing): a one-shot bootstrap that discovers an already-registered peer in the project scope **in-process** (`collectPeers`, no pipe → F-16 by design), derives its own name adaptively (inherits the peer's suffix — `VAL-x` ↔ `ESC-x` — or falls back to `<ROLE>-<scope-basename>`, converging in either order), registers idempotently (`--resume`, F-27), and then: for `val` sets `state=orchestrating` + exits, for `esc` hands off to `listen --wait-one`. Eliminates the manual register+peers+naming dance — the riskiest phase for a fresh agent.
- **F-41 — scope is the git REPOSITORY (git-common-root), not the physical checkout**: a linked `git worktree` resolves to its main repo, so a VAL at the repo root and an ESC in a worktree of the SAME repo share one scope and pair in plain `peers` — no flags. `resolveScope` symlink-canonicalizes (`EvalSymlinks`) so a repo reached through a symlink (e.g. macOS `/tmp` → `/private/tmp`) pairs too. Different git repos keep distinct scopes (isolated). `FindProjectRoot` stays a lexical walk; canonicalization happens downstream in its sole caller.
- **F-42 — `cab-bridge overview`**: me + paired peer + pending inbox in ONE call, no `--session-id`, human-readable (`--json` for scripting), worktree-aware. Collapses the `peers` + `whoami` + inbox-listing dance into one scannable view.
- **F-36 — `cab-bridge receive --any`**: id-less wake — blocks until the first batch of any non-ack message arrives (no `--in-reply-to` to fabricate), drains and archives it, emits NDJSON; an empty-window timeout exits 0 with `{"status":"timeout"}`. `type=ack` never wakes it (left in inbox as the F-12 delivery signal). The robust wake for an orchestrator with nothing specific to await. Mutually exclusive with `--msg-id`.
- **F-48 — `cab-bridge read <msg-id>` + `--emit=content`**: `read` prints a message's content body (`--json` for the full message) located by id in `inbox/` or `processed/` WITHOUT consuming it — matching on the decoded id so it finds archived messages despite their timestamp-prefixed filename. Replaces the `find … | python3` dance for a body the `inbox --list` preview (80 runes) truncates. `--emit=content` on `listen`/`receive` emits the body only (zero-parsing wakes; default `json` unchanged, timeout payload preserved). New exported `message.ValidateMessageID`.
- **F-43 — `ask` duplicate guard**: warns on stderr (and sends anyway) when an identical `(to, type, content)` message was sent to the same peer within `DedupWindowSeconds` (default 10, env `CAB_DEDUP_WINDOW_SECONDS`, ≤0 disables); `--skip-duplicate` skips the resend and prints the ORIGINAL id (caller idempotency). Defends against a degraded agent re-invoking `ask` before the first send's stdout returned (string-equality match, no hash, at outbox scale). The guard lives only in `runAsk` — the shared `sendMessage`/auto-ack is unaffected.
- **F-34 — unread-peer warning before `ask`**: warns on stderr (never blocks — sends anyway) when the recipient sent a still-unread non-ack message AFTER our last message to them — the dominant cause of VAL/ESC crossings (replying on a stale snapshot). Zero new state (inbox + outbox already present); the warning cites the id so the caller can `cab-bridge read <id>` first. Symmetric — fires for any sender.

### Changed
- **Skills realigned to v0.5**: the public `bridge-workflow` and the companion `cab-bridge-awareness` now cover `bootstrap`, scope=git-repo (worktree pairing), `overview`, `receive --any`, `read`/`--emit`, `state working/done` in the executor loop, and first-arriver-listens. The id-rule is refined: capturing an id from command output and reusing it is robust — only **inventing** one from memory is the hazard.

## [0.4.0] — 2026-05-31

Reliable wake/delivery cycle + inbox tooling + agent-state observability + idempotent reconnect. Built end-to-end via the cab-bridge dogfooding workflow and **validated in real use** (chatterence-bi field test: F-21/F-22/F-23a/F-26 confirmed on the job — "the bridge is now pleasant to use for an orchestrator, not just reliable"). Independent VAL gate `go test -race -count=1 ./...` green at each step + per-fix real smoke. Distilled from 8 independent dogfooding agent-voices (VPS + game + BI security review). Follow-up findings (F-34 conversation cursor, F-35 inbox filters, F-36 receive --any, F-23b read-receipt) deferred to a later version.

### Fixed
- **F-30 — `receive` no longer deletes the consumed reply, it archives it**: `scanForReply` did `os.Remove` on a match (asymmetric to `listen`/`DrainInboxOnce` which `MoveToProcessed`). With a background `receive` whose caller missed stdout, the reply was then gone from the inbox and survived only in the SENDER's outbox → recovery from someone else's path. Now it is moved to the receiver's own `processed/` dir, so recovery is from home. At-most-once preserved across concurrent callers (a lost archive race — `ErrNotExist` — keeps scanning instead of handing the reply out twice; EXDEV/permission returns the message anyway since the caller is blocking on exactly it). `send.go` untouched — the bug was consume-side, not delivery.

### Changed
- **F-24 — `listen --wait-one` exits 0 with a timeout payload instead of 124**: an empty-window timeout in `--wait-one` returned `ErrTimeout` → exit 124, which a run-in-background harness surfaced as "command failed" every idle cycle. It now emits `{"status":"timeout","messages":[]}` on stdout and exits 0; the caller tells a timeout from a delivered batch by the `status` field. The default `PollInbox` path keeps exit 124 (a bash until-loop relies on it).

### Added
- **F-26 — `listen --until-deadline=<duration>`**: explicit standby window (e.g. `2h`, `30m`) for the run-in-background executor pattern, more discoverable than `CAB_MAX_BLOCKING_SECONDS`. Precedence: `--until-deadline` flag > `CAB_MAX_BLOCKING_SECONDS` env > 540s default. Invalid/non-positive value is a hard error naming the flag.
- **F-22 — `inbox` subcommand**: `inbox --session-id=<id> --list [--json]` lists `inbox/` (pending) and `processed/` (consumed) messages WITHOUT consuming them (id, from, type, timestamp, one-line preview) — completes F-30 (an archived reply is listable from home, replacing the fragile `ls inbox/*.json`). `inbox --tidy` archives every well-formed `inbox/` message to `processed/` (lossless sweep via `MoveToProcessed`, the explicit "I handled what `--list` showed" hygiene action; malformed/`.tmp` files left for forensics). `--list` and `--tidy` are mutually exclusive.
- **F-23a — agent task-state observability**: new `state` field in the session manifest (`idle`/`working`/`done`/`orchestrating`), set with `cab-bridge state <value>`, shown in `peers` (new `STATE` column), `status`, `whoami`. **`orchestrating` is heartbeat-exempt** — an orchestrator (a VAL not in `listen`) no longer shows `stale` while working a gate ("orchestrator looks dead" fix). `IsStale` is now the single source of truth for staleness shared by `peers`/`status`/cleanup `globalSweep` (the three were divergent before). Schema additive (legacy sessions have empty `state` and behave exactly as before); the setter validates the enum, reads stay lenient (a newer peer's future state value never breaks our display). The 24h PID-dead auto-gc still reclaims a truly-abandoned orchestrator. Message-level read-receipt (F-23b: `delivered_at`/`read_at`) deferred to a later sprint.
- **F-27 — `register --resume` (reconnect-or-register)**: an idempotent bootstrap that resumes an existing session matching the identity `(agent-name, role, scope, team)` — reusing its sessionId, `inbox/`/`processed/`/`outbox/`, and `state` — instead of creating a new one, so a post-compact/restart agent recovers its identity in one line without knowing the old id. Liveness is the manifest PID via `IsProcessAlive` (the BUG-6 / auto-gc convention — NOT the lock, which `listen` does not hold): a live owner (a session with an active `listen`) is never stolen; if every identity match is live the command errors with `use --force-new` for a deliberate second instance. A legacy (pre-F-17) session resumed by a v0.4 binary has its `scope` backfilled (auto-migration to F-17). No identity match → registers fresh.

## [0.2.4] — 2026-05-30

Automatic per-project isolation. Built via the bridge dogfooding workflow.

### Added
- **Auto-scope by project root (F-17)**: `register` derives a `scope` from the project root — the `.git` marker walking up from cwd (a dir for a clone, a FILE for a git worktree), with `$HOME` excluded so a dotfiles repo never collapses scopes, and fallback = cwd for marker-less projects. A VAL at the repo root and an ESC in a subfolder resolve to the same scope and see each other with zero config. `whoami` now shows `scope`.

### Changed
- **`peers` default view is now scope-filtered** (was global): it shows only sessions whose scope matches the current cwd's project root, so multiple projects sharing one data dir no longer see each other's (often orphan) sessions. `peers --all-scopes` for the global listing; `--team` and `--all-scopes` bypass the scope filter. Sessions hidden by the filter are reported on stderr (no silent truncation). Manual `CAB_DATA_DIR`/`--team` isolation remains for special cases — F-17 makes the common case automatic.

## [0.2.3] — 2026-05-30

Prebuilt multi-OS binaries + public adoption. Built via the bridge dogfooding workflow.

### Added
- **GoReleaser** (`.goreleaser.yml` + `.github/workflows/release.yml`): prebuilt static binaries for darwin/linux × amd64/arm64 published to GitHub Releases on tag push (`CGO_ENABLED=0`, `-trimpath`, `checksums.txt`). Closes the source-first gap.
- **Version injection**: `main.version` is injected from the git tag (GoReleaser) / `git describe` (Makefile) — single source of truth is the tag; no more hand-bumping the binary version.
- **Public `bridge-workflow` skill** bundled with the plugin (`/cli-agents-bridge:bridge-workflow`): role-agnostic operating guide (PID/heartbeat model, `listen --wait-one`, team isolation, auto-ack, `cab sent`).

### Changed
- README role-agnostic (val/esc as example roles; documents free-form custom roles, `peer` for flat pairs, team isolation) + prebuilt-binary install path. `darwin-amd64` added to the cross-compile matrix (Makefile + ci.yml + goreleaser).

## [0.2.2] — 2026-05-30

Observability, instant wake, team isolation. Built end-to-end through the bridge dogfooding itself (a VAL↔ESC pair over `cab-bridge`).

### Added
- **F-12 task-state observability**: `ack` message type + automatic delivery receipt when `listen` consumes a query (allow-list `{query}`, loop-safe) + `inboxCount`/`lastConsumedMsgId` exposed in `peers`/`status`.
- **F-10 instant wake**: `listen --wait-one` exits (code 0) on the first non-empty batch (lossless drain-once) — wake-on-arrival for run-in-background callers.
- **F-5 team isolation**: `teamId` manifest field + `register --team=<name>` + `peers --team=<name>` filter + new `cab whoami` (full identity incl. full projectPath + dataDir).
- **F-9 self-send visibility**: each send is copied to the sender's outbox + new `cab sent`.

### Notes
- Security baseline SC-1..SC-7 unchanged; SECURITY.md keeps SC-3 honestly deferred (primitive present, runtime wiring not yet on the live path).

## [0.2.1] — 2026-05-29

First feature release after v0.2.0. Adds automatic orphan-session GC and closes a data-loss gap in session cleanup. Built end-to-end via the cli-agents-bridge dogfooding workflow (VAL↔ESC over the fork itself).

### Added
- **Auto-GC of orphan sessions** (`internal/cleanup/gc.go::GCOrphans`, F10): a session is removed only when **certainly orphaned** — owning PID dead (`session.IsProcessAlive`==false) **AND** `lastHeartbeat` older than `AutoGCHours`. The double condition (LL-10) is load-bearing: a just-`register`-ed session already has a dead PID (one-shot), so only a stale heartbeat distinguishes "abandoned" from "born seconds ago"; a session inside `listen` keeps a live PID (AdoptPID) and is never swept. Hooked into `register` (before creating the new session), opt-out via `AutoGCHours=0`. Every removal is logged to stderr (`auto-gc removed orphan session X (pid N dead, idle Hh)`) — no silent cleanup.
- Config `AutoGCHours` (json `auto_gc_hours`, env `CAB_AUTO_GC_HOURS`, default **24**).

### Fixed
- **Data-loss on session removal** (AUDIT-1, closes upstream pain §1.6): `archiveAndRemoveSession` previously archived only `processed/`, so `inbox/` + `outbox/` pending messages were deleted unarchived by `RemoveAll`. Now archives all three (`inbox`/`outbox`/`processed`) into `archive/<date>/<id>/<subdir>/` before removal. Applies to **all** cleanup paths (auto-gc, `--scope=my-session`, `--scope=global`). Verified: an orphan with an unread inbox message has it archived, not lost.
- **`default.json` not copyable to `config.json`** (F-A): the file carried a `"_comment"` key but the loader used `DisallowUnknownFields`, so copying it (as the comment itself suggested) failed with "unknown field". Added a no-op `Comment` field (`json:"_comment,omitempty"`) so the documented path works.
- **Config path ignored `CAB_DATA_DIR`** (F-B): `config.json` user-file path is now resolved from the env-overridden DataDir.

### Notes
- `peers` rejecting `--session-id` is intentional (it is a global command, not session-scoped) — kept as-is per LL-7. Backlog: a clearer error message + per-subcommand flag docs.

## [0.2.0] — 2026-05-29

First public release of cli-agents-bridge — fork of `PatilShreyas/claude-code-session-bridge` v0.1.0. The 9 confirmed upstream bugs fixed structurally (BUG-1..BUG-9), plus role-based routing, namespace-isolated storage, a security baseline (SC-1/2/4/5/6/7), and a single static Go binary distributed via self-marketplace GitHub.

Hardened pre-release across two gate passes: a triadic security audit (Sprint 5: SC-7 boot check, absolute DataDir, migrate integrity) and a manual smoke test (Sprint 6: BUG-A session-liveness PID model, BUG-B JSON hygiene). 158 tests pass under `-race`. SECURITY.md describes only controls actually on the live code path (SC-3 ownership wiring is honestly deferred to v0.2.1).

Sprint-by-sprint detail below (newest first).

### Sprint 6 — 2026-05-29 (fixup post-smoke, pre-tag)

#### Fixed
- **BUG-A** (session liveness): `register` is a one-shot command whose PID dies on return, so the BUG-6 collision check (`isProcessAlive`) never fired and sessions appeared STALE outside `listen`. `Manager.AdoptPID` is now called at `listen` startup so the long-running listener writes its own live PID into the manifest. Collision detection and stale detection observe a live owner during an active listen. `Touch` (connect path) deliberately unchanged. Caught by the manual smoke test, invisible to in-process unit tests.
- **BUG-B** (JSON hygiene): `cleanup.Result`, `migrateReport`, and `collectPeers` emitted nil slices serialising as JSON `null` instead of `[]` (breaking `jq '... | length'`). Slices initialised to `[]T{}` and the three `ErrNotExist` early-returns that overwrote the init with nil are fixed.

#### Changed
- `SECURITY.md` SC-6, `README.md` features, `docs/troubleshooting.md`: BUG-6 collision guarantee documented as **best-effort** (reliable against a live `listen` owner; a session with no listener is treated as abandoned and re-`register` is allowed, getting a fresh unique ID — sessions never merge). README "Security baseline" row corrected: removed "ownership check" (SC-3 is deferred v0.2.1).
- `commands/cab.md`: removed stale "Sprint 1 placeholder / Sprint 3" references.
- `tests/smoke-test.md`: pre-flight `CAB_DATA_DIR` fixed value `/tmp/cab-smoke-shared` (not `$$`, which differs per shell and isolates namespaces) + `make build && make install-dev` step + PATH-injection note.
- Plugin version `0.2.0-dev` → `0.2.0` in `plugin.json` + `marketplace.json`.

#### Tests
- +5 (AdoptPID writes live PID; integration: listen adoption enables real register collision; cleanup/peers/migrate empty buckets serialise as `[]`). Full suite 158 PASS / 0 FAIL under `-race`.

### Sprint 5 — 2026-05-28 (security hardening pre-tag)

#### Security
- **SC-7 base-dir integrity check** (`cmd/cab-bridge/common.go::bootstrapDataDir`): closes the declared-but-absent SC-7. Lstat-based, wired into all 10 subcommands. First-run creates dir 0o700; symlink / non-directory / owner-mismatch are FATAL (TM-5); loose perms trigger warn + chmod 0o700. Never auto-repairs a symlink.
- **DataDir absolute resolution** (`internal/config/config.go::Load`): a relative `CAB_DATA_DIR` or `data_dir` is resolved via `filepath.Abs` (+ warning) — prerequisite so SC-7 and every `filepath.Join` target the intended directory.
- **migrate-from-patil integrity** (`migrate.go`, `manager.go`): rejects manifests whose internal `sessionId` diverges from the directory name + re-validates it (SC-4); `migrateOne` calls `mf.Validate()` before writing (fail-closed); `LongestPrefixLookup` returns the directory name instead of the attacker-influenceable `mf.SessionID` (closes latent path-component propagation).

#### Changed
- `SECURITY.md` reconciled with the shipped binary: SC-7 documented as wired (now true); SC-3 (`CheckOwnership`) moved from "implemented P0" to "deferred v0.2.1" — the primitive exists and is tested but is not yet invoked at read call-sites. Primary TM-1/TM-6 defense in v0.2.0 is SC-1 + SC-2 + SC-7. Honesty note added (under-claim > over-claim).

#### Tests
- +12 (5 bootstrapDataDir unit, 2 config absolute-path, 1 LongestPrefixLookup NEW-1 regression, 2 SC-7 integration wiring, 2 migrate integrity). Full suite 153 PASS / 0 FAIL under `-race`.

#### Deferred to v0.2.1
- SC-3 ownership wiring via fstat-on-fd helper (closes Stat-vs-Lstat TOCTOU too), `os.Root` symlink-safe session creation, inbox backpressure bound, `scanForReply` idempotent-on-already-consumed.

### Sprint 4 — 2026-05-25 (release readiness)

#### Added
- 5 end-to-end integration test scenarios (PLAN §7.3): 1V+1E roundtrip, 1V+2E+observer routing enforcement, long-run heartbeat persistence (compressed), cross-project cleanup safety, migrate-from-patil dry-run + idempotent + RC-3 traversal reject + connect BUG-9 cmd-level
- `cab-bridge connect <peer-id>` subcommand — cmd-level wiring of `Manager.Touch` (BUG-9 fix end-to-end): refresh own heartbeat pre-handshake + `routing.ValidateSendPair` enforcement
- 8 production docs (vs Sprint 0 stub): README quickstart + feature parity table + roadmap; PRIVACY GDPR-1..5 with concrete commands; SECURITY threat model + 7 SC controls + disclosure policy; docs/migration-from-patil.md how-to with rollback; docs/multi-esc-patterns.md with hub-and-spoke vs mesh use cases; docs/security-model.md detailed TM-1..6 decomposition + code refs; docs/dev-conventions.md Go style + commit + test patterns; docs/troubleshooting.md FAQ
- tests/smoke-test.md — 15-step manual checklist (~45 min Alan-time) covering 5 setup + 5 happy + 5 edge cases (force-new collision, ESC→ESC forbidden, BUG-2 late reply recovery, BUG-7 stderr+exit124, cleanup global confirm + migrate dry-run)
- schemas/manifest-v2.schema.json + schemas/message-v2.schema.json — JSON Schema 2020-12 references with regex/enum constraints matching internal/security + internal/message validation gateway

#### Changed
- `cmd/cab-bridge/main.go` version bump 0.2.0-dev → **0.2.0** (release-ready)

### Sprint 3 — 2026-05-24 (MVP feature-complete)

#### Fixed
- **BUG-3** Multi-peer routing senza role disambiguation — `internal/routing/role.go::ValidateSendPair` hub-and-spoke val-centric + `--allow-mesh` override + observer-cannot-send STRUTTURALE (no flag override)
- **BUG-4** Cleanup globale cross-project — `internal/cleanup/scope.go` con scope=my-session default + scope=global TTY confirm + ErrConfirmRequired exit 3 non-tty + pre-delete archive `archive/<date>/<id>/` + retention sweep GDPR-1
- **BUG-8** STALE_SECONDS inconsistente — `config.StaleSeconds` unica fonte verità per peers cmd + cleanup library (zero hardcoded)
- **BUG-9** `connect-peer.sh` heartbeat sender — `Manager.Touch(sessionID)` single-shot heartbeat refresh

#### Added
- Inbox policy migration A→B: `poll.go` refactor `os.Remove` → `MoveToProcessed(processedDir)` con RFC3339 timestamp prefix (audit trail + recovery semantics + foundation transcript v0.3)
- 8 CMD subcommand suite: `register`, `listen`, `ask`, `peers`, `cleanup`, `status`, `inspect` (--json default per BUG R8 jq removal mitigation), `migrate-from-patil`
- `cmd/cab-bridge/common.go::exitFromErr` centralized error→exit mapping (124 timeout / 3 confirm-required / 1 validation / 0 success)
- `migrate-from-patil` subcommand Go: backup pre-migration + dry-run + idempotent (.migrated marker) + `--patil-dir` test injection + SC-4 path validation RC-3

### Sprint 2 — 2026-05-24

#### Fixed
- **BUG-2** `bridge-receive.sh` timeout secco — `ReceiveReply` long-poll fino a `--max-deadline` + preserva non-matching messages in inbox (recovery superior a Patil)
- **BUG-7** error su stdout — stderr-only + ErrTimeout sentinel → exit code 124 coreutils convention

#### Added
- Message schema v2 + `DecodeStrict` (gateway DisallowUnknownFields) / `DecodeLenient` (runtime read forward-compat schema additive) pattern
- Filesystem polling con `time.Ticker` + context cancellation + done channel idiom
- `cab-bridge receive --msg-id=X --max-deadline=N` subcommand
- `inReplyTo *string` pointer per Go-idiomatic JSON null semantics

### Sprint 1 — 2026-05-24

#### Fixed
- **BUG-1** Heartbeat dead in listen loop — `Manager.StartHeartbeat(ctx, sessionID) <-chan struct{}` con `time.Ticker` + done channel idiom
- **BUG-5** `get-session-id.sh` parent fallback — `Manager.LongestPrefixLookup` con `bestLen` + trailing-separator guard
- **BUG-6** Session ID collision per cwd — Lock O_EXCL atomic + `kill -0` ESRCH/EPERM stale recovery + ForceNew flag + holder PID in error message

#### Added
- Layout Patil-style refactor: `plugins/cli-agents-bridge/` subdir + Makefile `install-plugin` target
- Atomic write helper `internal/transport/fs/atomic.go`: same-dir mktemp + `f.Sync()` + Rename + EXDEV explicit "config bug not transient"

### Sprint 0 — 2026-05-24

#### Added
- Initial Go module baseline + security primitives P0 (umask 077, perms 700/600, ownership check, session ID regex validation)
- Day 0 FIX-4 spike: empirical verification of distribution path → Esito A definitivo self-marketplace
- Repo structure: cmd/, internal/, commands/, schemas/, tests/, config/, docs/
- CI matrix: cross-compile darwin-arm64 + linux-amd64 + linux-arm64 (no cgo)
- Docs stub: README, LICENSE MIT, ARCHITECTURE, CHANGELOG, PRIVACY, SECURITY

## [0.2.0] — TBD (Sprint 4 release)

Production release: integration test 5 scenari + docs production-ready + smoke test Alan + tag.
