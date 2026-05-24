# cli-agents-bridge

> Robust multi-peer IPC bridge between CLI agent sessions (Claude Code, etc.) — fork of [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) v0.1.0.

**Status**: v0.2.0-dev (Sprint 0). Not yet released.

## Why fork?

The upstream plugin has 9 bugs (6 critical + 3 additional) confirmed empirically across 15+ sub-sprint sessions. See [PLAN.md §2](./PLAN.md#2-validazione-bug-upstream) for the full validation.

`cli-agents-bridge` rewrites the bridge in **Go** (single static binary, no runtime deps) with:
- Heartbeat goroutine in listen loop (fix BUG-1)
- Long-poll receive with explicit timeout exit code (fix BUG-2/BUG-7)
- Role-based routing manifest schema v2 (fix BUG-3, Alan-reported "ESC→ESC accidental routing")
- Scoped cleanup (fix BUG-4, eliminates cross-project destructive cleanup)
- Longest-prefix-match session lookup (fix BUG-5)
- PID lock with `O_EXCL` semantics (fix BUG-6)
- Security baseline P0/P1 by default (umask 077, perms 700/600, ownership check, path traversal regex)
- Separate namespace `~/.claude/cli-agents-bridge/` (no shared dir with upstream Patil)

## Status & roadmap

See [ROADMAP.md](./ROADMAP.md) for milestones (v0.2.0 → v1.0.0).
See [PLAN.md](./PLAN.md) for the ratified design plan.

## License

MIT (compatible with upstream).
