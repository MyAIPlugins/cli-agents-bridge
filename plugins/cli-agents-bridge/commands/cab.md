---
description: cli-agents-bridge — show the working loop and the service commands
---

Invoke the cab-bridge binary help:

!`cab-bridge --help`

cab-bridge bridges CLI agent sessions (Claude Code, Codex, Aider, ...) over filesystem IPC — no network, no API calls.

**The working loop is five commands, no flags**: `join` once at the start, then `next` forever, and `ask` / `tell` / `reply` to talk. The verb carries the type, so there is nothing to configure and no id to type: recipients are agent names.

**Service commands** — `read`, `sent`, `peers`, `overview`, `whoami`, `state`, `status`, `inbox`, `cleanup`, `inspect`, `notify-watch` — are for inspection and maintenance, never in the loop. Each prints its own `--help`.

For the mailbox model (`UNREAD → NOTIFIED → ARCHIVED`, where `next` never moves a file and only `reply` archives), see the `bridge-workflow` skill.
