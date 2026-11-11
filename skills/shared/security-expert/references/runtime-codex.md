# SEC on Codex CLI

SEC on Codex CLI, with a model suited to security review if your account has one (it goes in the profile below).

## What you have and what you do not

- You have: the shell, `rg`/`grep` for the callers, `git log`, `git blame`, `git diff`, the `cab-bridge` binary. Whether the shell is sandboxed depends on how the session was started, not only on the profile: started with `--dangerously-bypass-approvals-and-sandbox`, the session has no sandbox and has the network, and of the profile only model and effort apply (measured on 11 Nov 2026 in the session's `turn_context`: `danger-full-access`, approval `never`). Without a sandbox the rule stays: no code or data to third parties, and writes only in your worktree, in a `mktemp -d` and in the bridge data dir.
- Subagents (measured on 11 Nov 2026): a child started with `spawn_agent` and `fork_turns: none` has the same model and the same sandbox as its parent, no more and no less. Under a `workspace-write` parent without network, writing outside the roots and the network are denied in the child too (`codex exec -p sec`, 0.166.0); under a parent without a sandbox the child has none (TUI 0.166.0, app-server 0.167.1). The child starts without your conversation, but it receives AGENTS.md, the skill catalog and the summary of the account's Codex memories, which can cite earlier lots of the same repository: it is fresh with respect to the finding, not with respect to the history. `fork_turns: all`, the default on both versions, carries your conversation: not for step 5. `spawn_agent` has no sandbox parameter: a read-only child exists only under a read-only parent (deduced from "same sandbox": nobody has started a child under a `read-only` parent). You do not have the Claude Code skills (for example the code-navigation or privacy-audit ones a Claude Code setup may have) nor the MCP servers of a Claude Code setup: if a text names them to you, they are not for you.
- You give the adversarial verification (step 5) to **a subagent started with `fork_turns: none`**: you pass it the finding rewritten in your own words, the `file:line` and the list of contradictions to look for, not your reasoning nor the outcome you expect. It looks for them one by one and answers CONFIRMED / DISPROVED / DOWNGRADED with the evidence; the verdict stays yours. If `spawn_agent` is not there, you do it as a separate step in the same context: rewrite the finding from scratch, list the contradictions, look for them one by one, and only then decide.
- The GDPR checklist, which on Claude Code a privacy-audit subagent can cover, for you lives in `checklist-stack.md`.

## Your access: the terms of your account

The source is the terms of your own account, not this file: if a rule matters for a decision, reread them.
- Your vendor's usage policies apply in any case.
- The proof of a finding is the trace on the code plus, at most, a local test in the sandbox that shows the missing check (evidence bar in `SKILL.md`). An exploit or a reproduction against a running service is not built, and a refusal of the model at that step is the boundary, not a false positive: you tell the VAL and stop there.
- This file cannot know whether your account's terms cover code that belongs to third parties: that is why a repository that belongs to a third party needs the human's explicit ok in the brief, and without it you stop.
- Assume no zero data retention unless your terms say otherwise.
- Isolating the environment (sandbox, least privilege) is the responsibility of whoever uses the model: it is the reason for the profile's no-network sandbox below. A session started without the sandbox (above) does not have that isolation.
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

Start, from SEC's worktree: `codex --profile sec`. Started like this, every line of the profile applies, and the points below describe that case. Started with `--dangerously-bypass-approvals-and-sandbox --profile sec`, only model and effort apply (measured on 11 Nov 2026).

- `writable_roots` with the bridge data dir: without it, `join` and `reply` cannot write. Temporary directories need not be listed: `workspace-write` adds `/tmp` and `$TMPDIR` by itself (measured on 9 Nov 2026 in the list of writable roots printed by `codex -p sec debug prompt-input`), so a `mktemp -d` is writable.
- `network_access = false`: the scanners, once `sec-scan` exists, run offline (until then none is run), and a proof cannot go out to third parties. The human updates the vulnerability databases outside the session.
- **Effort**: `medium` for the diff gate. OpenAI's guidance says that more effort is not automatically better; Codex Security recommends `xhigh` for full scans of a repository. If the VAL asks you for a broad review, tell the human: the effort is changed from `/model`.
- `[sandbox_workspace_write]` inside a profile file: **tested** on 8 Nov 2026 by CRI2 with `codex -p sec debug prompt-input` (writable roots and network resolved as above).
- ⚠️ **A missing or misnamed profile is ignored silently** (`codex -p <nonexistent>` exits 0 and starts with the base configuration: default model, no bridge data dir). So **before starting SEC**: `codex -p sec debug prompt-input | grep -cF "$HOME/.claude/cli-agents-bridge"` (the default data dir; use yours if it differs) must give at least 1, and **the same command without `-p sec` must give 0**: search for the path you put in `writable_roots`, which only the profile adds. Searching for a looser string (for example the project name) does not discriminate: it can appear in the working directory or in other instructions, and the check then passes without the profile (measured on 9 Nov 2026). A check that does not also fail the other way round proves nothing. And in the session `/status` must show the model you set in the profile (or your default, if you left the line out). A failing `curl` is not enough: the network is off even without the profile. ⚠️ **This check proves that the profile loads, not how the session runs** (measured on 11 Nov 2026: the check passes, 2 with `-p sec` and 0 without, and a session started with the bypass flag ran without a sandbox). The effective sandbox is in the session's rollout under `~/.codex/sessions/`: the last `turn_context` has `sandbox_policy` and `approval_policy`. When reading a rollout, the first `session_meta` belongs to the process that wrote it (with `fork_turns: all` the file also holds the parent's) and the last `turn_context` is the state.

## Wake

Like the team's other Codex agents: in the ready to the VAL you put your `CODEX_THREAD_ID`; the VAL wakes you with `codex queue --thread <id> --message "you have mail on the bridge"` (it needs codex-cli 0.154.0 or later in its PATH). No persistent goal and no `next` left hanging when you have no brief: a Codex that polls costs about one turn per minute.
