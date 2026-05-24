# Architecture

> Stub Sprint 0. This document will be expanded post-v0.2.0 release by merging final design decisions from [PLAN.md](./PLAN.md).

For the current ratified design see [PLAN.md v3](./PLAN.md).

## Quick reference

- **Language**: Go (single static binary, `golang.org/x/sys/unix` for portable syscalls, no cgo)
- **Transport** (MVP): filesystem polling JSON in `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox}/`
- **Transport** (v0.4 gated): optional Unix socket daemon on `/tmp/cab-$UID/cab.sock`
- **Storage**: namespace separato da Patil upstream (no shared dir)
- **Distribution**: self-marketplace GitHub primary, pure-PATH fallback (Day 0 spike determines)
- **Security**: P0 baseline (umask 077, perms 700/600, ownership check, session ID regex, atomic write)
- **Schema**: manifest v2 trimmed (4 new fields: schemaVersion, role, agentName, pid)
