# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Sprint 0 — 2026-05-24

- Initial Go module baseline + security primitives (umask, perms, ownership check, session ID regex validation)
- Day 0 FIX-4 spike: empirical verification of distribution path (self-marketplace vs pure-PATH fallback)
- Repo structure §4.2 (cmd/, internal/, commands/, schemas/, tests/, config/, docs/)
- CI matrix: cross-compile darwin-arm64 + linux-amd64 + linux-arm64 (no cgo)

## [0.2.0] — TBD (Sprint 1-7 target)

- Fix BUG-1..BUG-9 (heartbeat, long-poll, role routing, scoped cleanup, longest-prefix, PID lock, stderr exit codes, config unified)
- Schema v2 manifest + message (additive backward-compat read)
- Migration subcommand `cab-bridge migrate-from-patil`
- 9 regression tests + 5 integration scenarios + manual smoke test
