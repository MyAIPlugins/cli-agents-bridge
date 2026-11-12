# Agent setup: install and verify cli-agents-bridge

For people: copy the prompt below into a coding agent (Claude Code, Codex CLI, or any agent with a shell) and it does the setup and tells you what it checked. For agents: everything you need is on this page.

This file is **not** loaded by agents on their own, on purpose: a repository that ships `AGENTS.md` or `CLAUDE.md` gives instructions to the agent of everyone who clones it. You read this page because a person asked you to.

## The prompt

```
Set up cli-agents-bridge (the `cab-bridge` command) on this machine.

Instructions: https://raw.githubusercontent.com/myAIPlugins/cli-agents-bridge/main/docs/agent-setup.md
Read that whole file before you run anything, then:

1. Detect the OS and CPU architecture. If `cab-bridge version` already prints a version,
   say which one and where the binary is, and skip to step 3.
2. Install the binary of the latest release for this system, with the commands in
   "Install the binary". Verify the checksum. Check `cab-bridge version` in a NEW shell.
3. Install the skill for the agent runtime you are, as in "Skills" (for Claude Code: ask me
   to run the two plugin commands). If a skill folder with the same name already exists
   and differs from the repository's, stop and ask me.
4. Run the "Smoke test" in a temporary folder. It must not touch my real bridge data.
5. Do not run `cab-bridge cleanup`, do not edit claude_desktop_config.json, and do not
   join a real session: I will tell you the role and name to use.
6. Report: OS and architecture, binary path and version, the skills you installed and
   where, the smoke test output, and anything you could not verify.
```

## What works where

| | macOS | Windows | Linux |
|---|---|---|---|
| The bridge between terminal agents (`join`, `next`, `ask`, `tell`, `reply`) | yes | yes | yes |
| Release binary | arm64, amd64 | amd64 only | arm64, amd64 |
| Waking a Codex CLI peer with `codex queue` | yes | yes | yes (1) |
| `cab-bridge notify-watch` (run a hook when mail arrives) | yes | no: refuses with an error | yes |
| Claude Desktop through `cab-bridge mcp` | yes | no: refuses with an error | yes (2) |
| `make install-skills` | yes | no: copy the folders by hand | yes |
| Claude Code plugin (`/cli-agents-bridge:cab`, `bridge-workflow`) | yes | yes (1) | yes (1) |

(1) Expected to work, not yet exercised on that system by the maintainers. (2) The server runs on Linux; Claude Desktop itself must be available there.

The bridge, its tests and the release are checked on all three systems in CI.

## Install the binary

Release archives: `cab-bridge_<version>_<os>_<arch>.tar.gz` (macOS, Linux) and `cab-bridge_<version>_windows_amd64.zip`, with `checksums.txt`. A matching checksum proves the file arrived intact, not that the release is genuine: the checksums come from the same place. There is no signature yet.

macOS and Linux:

```sh
cd "$(mktemp -d)"
REPO=myAIPlugins/cli-agents-bridge
TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep -m1 '"tag_name"' | cut -d'"' -f4)
VERSION=${TAG#v}
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64; [ "$ARCH" = aarch64 ] && ARCH=arm64
ARCHIVE="cab-bridge_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "https://github.com/$REPO/releases/download/$TAG/$ARCHIVE"
curl -fsSLO "https://github.com/$REPO/releases/download/$TAG/checksums.txt"
if command -v sha256sum >/dev/null; then grep " $ARCHIVE\$" checksums.txt | sha256sum -c -; else grep " $ARCHIVE\$" checksums.txt | shasum -a 256 -c -; fi
tar -xzf "$ARCHIVE" cab-bridge
mkdir -p ~/.local/bin && install -m 755 cab-bridge ~/.local/bin/
```

The checksum line must print `OK`; anything else, stop. The commands run in a new temporary folder because the archive also holds `README.md` and `LICENSE`, and the downloads would land in whatever folder you are in.

`~/.local/bin` must be on `PATH`; on macOS it usually is not. If it is missing, ask the person before adding `export PATH="$HOME/.local/bin:$PATH"` to their shell profile (`~/.zshrc` for zsh, `~/.bashrc` for bash). Until then, call the binary by its full path.

Windows (Windows PowerShell 5.1 or PowerShell 7, nothing to install first):

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$ProgressPreference = 'SilentlyContinue'
$Work    = [IO.Path]::Combine([IO.Path]::GetTempPath(), "cab-install-" + [guid]::NewGuid().ToString('N').Substring(0,8))
New-Item -ItemType Directory $Work | Out-Null; Set-Location $Work
$Repo    = 'myAIPlugins/cli-agents-bridge'
$Tag     = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name
$Ver     = $Tag.TrimStart('v')
$Zip     = "cab-bridge_${Ver}_windows_amd64.zip"
Invoke-WebRequest "https://github.com/$Repo/releases/download/$Tag/$Zip" -OutFile $Zip -UseBasicParsing
Invoke-WebRequest "https://github.com/$Repo/releases/download/$Tag/checksums.txt" -OutFile checksums.txt -UseBasicParsing
$want = ((Select-String -Path checksums.txt -Pattern " $([regex]::Escape($Zip))$").Line -split '\s+')[0]
$got  = (Get-FileHash $Zip -Algorithm SHA256).Hash.ToLower()
if ($want -ne $got) { throw "checksum mismatch: expected $want, got $got" }
Expand-Archive $Zip -DestinationPath .\cab-bridge -Force
$dir = "$env:LOCALAPPDATA\Programs\cab-bridge"
New-Item -ItemType Directory -Force $dir | Out-Null
Copy-Item .\cab-bridge\cab-bridge.exe $dir -Force
$current = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($current -notlike "*$dir*") { [Environment]::SetEnvironmentVariable('Path', "$current;$dir", 'User') }
```

The first two lines are for Windows PowerShell 5.1, the one every Windows has: it may not offer TLS 1.2 to GitHub, and its progress bar slows the download a lot. The work happens in a new temporary folder, as on macOS and Linux. Read the current user `PATH` and append, as above. Never `setx PATH "..."`: it replaces the user's whole `PATH`, silently. Then open a **new** terminal: a running one keeps the environment it started with.

The latest release can be behind `main`. To use `main`, build from source with Go 1.25 or later: `make build` on macOS and Linux (puts `bin/cab-bridge`), or on Windows `go build -o bin\cab-bridge.exe ./cmd/cab-bridge`, then copy the binary as above.

## Skills

The binary needs no skill to run. A skill tells the agent the protocol, so it does not have to infer it from `--help`. Skills and plugin come from `main`, the binary from the latest release: where they disagree, the binary's `--help` is right. Install the one for your runtime:

| Runtime | Skill | Where it goes |
|---|---|---|
| Claude Code | `bridge-workflow` | the plugin installs it: ask the person to run `/plugin marketplace add myAIPlugins/cli-agents-bridge` then `/plugin install cli-agents-bridge@cli-agents-bridge-marketplace` (an agent cannot run slash commands) |
| Codex CLI | `skills/codex/cab-bridge-awareness` | `~/.agents/skills/cab-bridge-awareness/` |
| Antigravity | `skills/antigravity/cab-bridge-awareness` | `~/.gemini/config/skills/cab-bridge-awareness/` |
| Claude Desktop | `skills/claude-desktop/arc-bridge` | a zip of the folder, uploaded by the person under Customize > Skills |

Role skills (a critic, a security reviewer, the team's rubrics) are in `skills/claude/`, `skills/codex/` and `skills/shared/`; `skills/shared/<name>/` goes to both `~/.claude/skills/` and `~/.agents/skills/`. Install them only if the person asks for those roles.

To install one skill, clone the repository in a temporary folder and copy that folder. If the destination folder already exists, compare it first (`diff -r` on macOS and Linux) and, if it differs, stop and ask. The Codex skill, for example; for Antigravity use `skills/antigravity/cab-bridge-awareness` and `~/.gemini/config/skills`:

```sh
cd "$(mktemp -d)" && git clone -q --depth 1 https://github.com/myAIPlugins/cli-agents-bridge.git
mkdir -p ~/.agents/skills
cp -R cli-agents-bridge/skills/codex/cab-bridge-awareness ~/.agents/skills/
```

```powershell
$Work = [IO.Path]::Combine([IO.Path]::GetTempPath(), "cab-skills-" + [guid]::NewGuid().ToString('N').Substring(0,8))
New-Item -ItemType Directory $Work | Out-Null; Set-Location $Work
git clone -q --depth 1 https://github.com/myAIPlugins/cli-agents-bridge.git
$Dest = [IO.Path]::Combine($HOME, '.agents', 'skills')
New-Item -ItemType Directory -Force $Dest | Out-Null
Copy-Item -Recurse ([IO.Path]::Combine('cli-agents-bridge', 'skills', 'codex', 'cab-bridge-awareness')) $Dest
```

On Windows, if the destination folder exists, move it aside before copying: `Copy-Item -Recurse` into an existing folder nests it (`cab-bridge-awareness\cab-bridge-awareness`) instead of replacing it.

`make install-skills` (macOS and Linux, from a clone) installs **every** skill of the repository, role skills included, for every runtime it finds: use it only if the person wants all of them. `make skills-check` shows what it would change. Both refuse to overwrite a copy that differs from the repository's unless `FORCE=1`: do not pass `FORCE=1` without the person's yes. Without Go installed, `make` prints `go: Command not found` a few times; the skill targets do not need Go.

Codex reads `~/.agents/skills` and also the older `~/.codex/skills`. A skill in both is listed twice: keep one copy, in `~/.agents/skills`.

## Smoke test

Two agents in two folders of one git repository, with the bridge data in a temporary folder (`CAB_DATA_DIR`) so the person's real sessions are not touched. No process runs in the background: the message is sent first, then read.

macOS and Linux:

```sh
T=$(mktemp -d)
mkdir -p "$T/repo/a" "$T/repo/b" && git -C "$T/repo" init -q
export CAB_DATA_DIR="$T/data"
(cd "$T/repo/a" && cab-bridge join --role=val --agent-name=smoke-a)
(cd "$T/repo/b" && cab-bridge join --role=esc --agent-name=smoke-b)
(cd "$T/repo/b" && cab-bridge tell smoke-a "ping")
(cd "$T/repo/a" && cab-bridge next)
unset CAB_DATA_DIR
```

Windows:

```powershell
$T    = [IO.Path]::Combine([IO.Path]::GetTempPath(), "cab-smoke-" + [guid]::NewGuid().ToString('N').Substring(0,8))
$Repo = [IO.Path]::Combine($T, 'repo')
$A    = [IO.Path]::Combine($Repo, 'a')
$B    = [IO.Path]::Combine($Repo, 'b')
New-Item -ItemType Directory $A, $B -Force | Out-Null
git -C $Repo init -q
$env:CAB_DATA_DIR = [IO.Path]::Combine($T, 'data')
Push-Location $A; cab-bridge join --role=val --agent-name=smoke-a; Pop-Location
Push-Location $B; cab-bridge join --role=esc --agent-name=smoke-b; cab-bridge tell smoke-a "ping"; Pop-Location
Push-Location $A; cab-bridge next; Pop-Location
Remove-Item Env:CAB_DATA_DIR
```

The paths are built with `[IO.Path]::Combine` because `git` and `cab-bridge` receive them as text: a literal `\` is a separator only on Windows.

Pass: the second `join` lists `smoke-a`, and `next` prints a JSON document with `"status": "emitted"` and `"content": "ping"`, then one with `"status": "committed"`. `tell` adds a note that no `next` is listening: expected, the message waits in the inbox. Two agents in the **same** folder are one session, not two: the second `join` renames the first. The temporary folder can be deleted afterwards.

## The protocol, in short

The rules are in the skill; load it rather than inferring them from here.

- Five commands: `join` once, then `next` in a loop, with `ask`, `tell` and `reply` to talk. Long messages from stdin: `cab-bridge tell <name> < report.md`.
- `next` never consumes: it shows. Only `reply` archives, and anything that arrived later is handed to you again, marked `redelivered`.
- Arm the next `next` only after reading the output of the previous one. A second `next` on the same session interrupts the first.
- Claude Code: run `next` in the background and you are woken when it returns. Codex CLI has no push: give the orchestrator your `CODEX_THREAD_ID`, stay idle, and it wakes you with `codex queue --thread <id> --message "..."`.
- Recipients are agent names; `<name>@<project>` reaches another repository (orchestrator to orchestrator only).

Roles are free-form. Built in: `val` (orchestrator), `esc` (executor), `critic`, `observer` (cannot send), `neutral`, and `architect` for Claude Desktop. `esc` to `esc` is refused unless `--allow-mesh`; `peer` to `peer` is allowed. The scope is the git repository root: agents in the same repository, worktrees included, see each other; other repositories do not, unless addressed with `<name>@<project>` or listed with `peers --all-scopes`.

## Where the data lives

```
~/.claude/cli-agents-bridge/          base; CAB_DATA_DIR points here, never at layout-2
├── config.json                       optional; CAB_* variables override it
└── layout-2/sessions/<id>/           since v0.12.0; v0.11.0 and earlier use sessions/<id>/
    ├── manifest.json
    ├── inbox/  outbox/  processed/
    └── lock                          empty file, held with a kernel lock
```

`cab-bridge whoami` prints the session, the data folder and the base. Every command prints its own `--help`.

## All commands

```
cab-bridge join --role=<role>   Once, at the start: register (idempotent) and see who is here
cab-bridge next                 Then forever: deliver whatever arrived, waiting until it does
cab-bridge ask <agent> ["msg"]  Ask something: stays open until they reply
cab-bridge tell <agent> ["msg"] Inform: no reply expected
cab-bridge reply ["msg"]        Answer whoever asked; closes the asks that came in together
```

Without a message argument the text is read from stdin. Service commands, never in the loop:

```
cab-bridge read <msg-id>        Re-read a message, including an archived one
cab-bridge sent                 What I sent and what state it is in for the recipient
cab-bridge peers                Who exists (table, or --json / --all-scopes)
cab-bridge overview             Me, my peers and my inbox at a glance
cab-bridge whoami               This session's identity
cab-bridge state <value>        Declare what I am doing: idle | working | done | orchestrating
cab-bridge status               Own session counters
cab-bridge inbox --list|--tidy  Inspect without consuming, or archive what is handled
cab-bridge register             Lower-level registration (join is the way in)
cab-bridge connect <peer-id>    Refresh own heartbeat and check a peer is reachable
cab-bridge notify-watch -- <hook>  Run <hook> when mail arrives (not on Windows)
cab-bridge desktop open|close|status  Open this project to Claude Desktop (a val runs it)
cab-bridge mcp                  The MCP server Claude Desktop starts; not run by hand
cab-bridge findings add|stale   The team's findings registry, .handover/findings.tsv
cab-bridge cleanup              Remove own session, or --scope=global (asks first)
cab-bridge inspect <id>         Print a session manifest
cab-bridge migrate-from-patil   Import sessions of the session-bridge plugin
cab-bridge version              Show version
```

## Do not, without the person's yes

- `cab-bridge cleanup --scope=global`, in any form: it can remove the session of an agent that is idle but reachable.
- Edit `claude_desktop_config.json`: print the entry from [claude-desktop.md](./claude-desktop.md) and let the person add it.
- Overwrite a skill folder that differs from the repository's, or pass `FORCE=1`.
- Set `session_lock_wait_ms` or other new keys in `config.json` while an older `cab-bridge` is still installed: older binaries reject unknown keys and then every command fails.
