# SEC on Codex CLI

SEC on Codex CLI, with a model suited to security review if your account has one (it goes in the profile below).

## What you have and what you do not

- You have: the shell in the sandbox (no network), `rg`/`grep` for the callers, `git log`, `git blame`, `git diff`, the `cab-bridge` binary.
- Subagents: Codex has them (measured on 9 Nov 2026, `codex features list` on 0.166.0: `multi_agent stable true`), but this role on Codex does not use them: you do the adversarial verification in the same context (below). You do not have the Claude Code skills (for example the code-navigation or privacy-audit ones a Claude Code setup may have) nor the MCP servers of a Claude Code setup: if a text names them to you, they are not for you.
- You do the adversarial verification (step 5) as a **separate step in the same context**: rewrite the finding from scratch in your own words, list the contradictions to look for, look for them one by one, and only then decide CONFIRMED / DISPROVED / DOWNGRADED.
- The GDPR checklist, which on Claude Code a privacy-audit subagent can cover, for you lives in `checklist-stack.md`.

## Your access: the terms of your account

The source is the terms of your own account, not this file: if a rule matters for a decision, reread them.
- Your vendor's usage policies apply in any case.
- The proof of a finding is the trace on the code plus, at most, a local test in the sandbox that shows the missing check (evidence bar in `SKILL.md`). An exploit or a reproduction against a running service is not built, and a refusal of the model at that step is the boundary, not a false positive: you tell the VAL and stop there.
- This file cannot know whether your account's terms cover code that belongs to third parties: that is why a repository that belongs to a third party needs the human's explicit ok in the brief, and without it you stop.
- Assume no zero data retention unless your terms say otherwise.
- Isolating the environment (sandbox, least privilege) is the responsibility of whoever uses the model: it is the reason for the no-network sandbox below.
- A safeguard that triggers on legitimate defensive work (a false positive of the safeguard, not the boundary above): `/feedback` in Codex, when it is available, and you tell the VAL.

## How it is configured (the human installs it)

Since 0.134.0 a Codex profile is a separate file, not a `[profiles.x]` table in `config.toml` (which with `--profile` makes the loading fail). The file `~/.codex/sec.config.toml` (example):

```toml
model = "<model id>"   # a model suited to security review, if your account has one; look it up in the catalogue with `codex debug models`; without one, leave this line out
model_reasoning_effort = "medium"
approval_policy = "on-request"
sandbox_mode = "workspace-write"

[sandbox_workspace_write]
network_access = false
writable_roots = ["<absolute path of your home>/.claude/cli-agents-bridge"]
```

Start, from SEC's worktree: `codex --profile sec`.

- `writable_roots` with the bridge data dir: without it, `join` and `reply` cannot write. Temporary directories need not be listed: `workspace-write` adds `/tmp` and `$TMPDIR` by itself (measured on 9 Nov 2026 in the list of writable roots printed by `codex -p sec debug prompt-input`), so a `mktemp -d` is writable.
- `network_access = false`: the scanners, once `sec-scan` exists, run offline (until then none is run), and a proof cannot go out to third parties. The human updates the vulnerability databases outside the session.
- **Effort**: `medium` for the diff gate. OpenAI's guidance says that more effort is not automatically better; Codex Security recommends `xhigh` for full scans of a repository. If the VAL asks you for a broad review, tell the human: the effort is changed from `/model`.
- `[sandbox_workspace_write]` inside a profile file: **tested** on 8 Nov 2026 by CRI2 with `codex -p sec debug prompt-input` (writable roots and network resolved as above).
- ⚠️ **A missing or misnamed profile is ignored silently** (`codex -p <nonexistent>` exits 0 and starts with the base configuration: default model, no bridge data dir). So **before starting SEC**: `codex -p sec debug prompt-input | grep -cF "$HOME/.claude/cli-agents-bridge"` (the default data dir; use yours if it differs) must give at least 1, and **the same command without `-p sec` must give 0**: search for the path you put in `writable_roots`, which only the profile adds. Searching for a looser string (for example the project name) does not discriminate: it can appear in the working directory or in other instructions, and the check then passes without the profile (measured on 9 Nov 2026). A check that does not also fail the other way round proves nothing. And in the session `/status` must show the model you set in the profile (or your default, if you left the line out). A failing `curl` is not enough: the network is off even without the profile.

## Wake

Like the team's other Codex agents: in the ready to the VAL you put your `CODEX_THREAD_ID`; the VAL wakes you with `codex queue --thread <id> --message "you have mail on the bridge"` (it needs codex-cli 0.154.0 or later in its PATH). No persistent goal and no `next` left hanging when you have no brief: a Codex that polls costs about one turn per minute.
