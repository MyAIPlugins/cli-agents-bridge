# cli-agents-bridge

Let your AI coding agents talk to each other. Run Claude Code, Codex CLI or any agent with a shell in separate terminal windows on the same computer, and they can hand each other work, ask questions and report back, without you copying text between windows.

**Status**: v0.11.0 (shipped). See [CHANGELOG.md](./CHANGELOG.md) and [Releases](https://github.com/MyAIPlugins/cli-agents-bridge/releases).

**Setting it up with an agent?** Skip to [For agents](#for-agents).

---

## What it does

A common way to work with coding agents is to give them roles: one plans and reviews, another writes the code, a third looks for problems. Each agent runs in its own window, with its own conversation. cli-agents-bridge is the mailbox between them:

- **One small program, `cab-bridge`.** No server, no account, nothing to keep running.
- **Everything stays on your computer.** Messages are files in a folder in your home directory, readable only by you on macOS and Linux (Windows: see [Your data](#your-data)).
- **Nothing gets lost.** A message stays in the agent's inbox until that agent answers it. If an agent is busy, restarted or compacted, the message is still there.
- **Agents find each other by name**, inside the same project. Different projects stay separate.

The bridge itself makes no network calls. What an agent reads from it becomes part of that agent's conversation, so it reaches the agent's provider like anything else the agent reads.

## What works where

| | macOS | Windows | Linux |
|---|---|---|---|
| Agents in terminals talking to each other (the core) | Yes | Yes | Yes |
| Ready-made download | Apple Silicon and Intel | x64 only | x64 and ARM |
| Claude Code plugin (skill and `/cab` command) | Yes | Yes (1) | Yes (1) |
| Waking a Codex CLI agent when a message arrives | Yes | Yes | Yes (1) |
| Running your own command when a message arrives (`notify-watch`) | Yes | No | Yes |
| Claude Desktop joining the conversation | Yes | No | Yes (2) |
| Installing the skills with one command | Yes | No: copy the folders | Yes |

(1) Expected to work, not yet tried on that system by the maintainers.
(2) The bridge side runs on Linux; Claude Desktop itself has to be available there.

The test suite runs on macOS, Windows and Linux for every change. What "No" means: the command exists but refuses to run with an error that says so, nothing breaks silently.

---

## Install

Three steps: get the program, put it where your terminal finds it, check it runs. About five minutes.

You can also let an agent do it for you: see [For agents](#for-agents).

### 1. Download

Open the latest [Release](https://github.com/myAIPlugins/cli-agents-bridge/releases/latest) and download the file for your system:

| System | File |
|---|---|
| Mac with Apple Silicon (M1 and later) | `cab-bridge_<version>_darwin_arm64.tar.gz` |
| Mac with Intel | `cab-bridge_<version>_darwin_amd64.tar.gz` |
| Windows | `cab-bridge_<version>_windows_amd64.zip` |
| Linux | `cab-bridge_<version>_linux_amd64.tar.gz` or `_linux_arm64.tar.gz` |

Download `checksums.txt` too, if you want to check that the file arrived intact.

### 2. Install

**macOS and Linux.** In a terminal, in the folder where you downloaded the file:

```sh
VERSION=PUT_THE_VERSION_HERE   # the version WITHOUT the leading v: the tag has it, the file name does not
OS=darwin                      # darwin (Mac) | linux
ARCH=arm64                     # arm64 | amd64

tar -xzf "cab-bridge_${VERSION}_${OS}_${ARCH}.tar.gz" cab-bridge
mkdir -p ~/.local/bin
install -m 755 cab-bridge ~/.local/bin/
```

`~/.local/bin` must be on your `PATH`. On a Mac it usually is not: add it with `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc` (on Linux with bash, `~/.bashrc`), then open a new terminal. If step 3 says `command not found`, this is the reason.

**Windows.** In PowerShell, in the folder where you downloaded the file:

```powershell
$Version = 'PUT_THE_VERSION_HERE'   # the version WITHOUT the leading v: the tag has it, the file name does not
$Zip     = "cab-bridge_${Version}_windows_amd64.zip"

Expand-Archive $Zip -DestinationPath .\cab-bridge -Force
$dir = "$env:LOCALAPPDATA\Programs\cab-bridge"
New-Item -ItemType Directory -Force $dir | Out-Null
Copy-Item .\cab-bridge\cab-bridge.exe $dir -Force

# Add the folder to your user PATH, keeping what is already there
$current = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($current -notlike "*$dir*") { [Environment]::SetEnvironmentVariable('Path', "$current;$dir", 'User') }
```

Do not use `setx PATH "..."` instead: it replaces your whole user `PATH`, silently. Then **open a new terminal**: a window that was already open keeps the old `PATH`.

**Checking the download (optional).** Compare the file's SHA-256 with its line in `checksums.txt`: `shasum -a 256 <file>` on macOS, `sha256sum <file>` on Linux, `Get-FileHash <file>` in PowerShell. A match proves the file arrived intact, not that it is genuine: both come from the same place, and there is no signature yet.

### 3. Check it runs

```
cab-bridge version
```

It must print a version. If it does not, fix the `PATH` before going on.

### 4. Teach your agents the protocol

The program works on its own, but an agent uses it well only if it knows the rules. Those come as a **skill**, one per agent runtime:

- **Claude Code**: install the plugin, from a Claude Code session:
  ```
  /plugin marketplace add myAIPlugins/cli-agents-bridge
  /plugin install cli-agents-bridge@cli-agents-bridge-marketplace
  ```
  It adds the `bridge-workflow` skill and the `/cli-agents-bridge:cab` command.
- **Codex CLI, Antigravity, Claude Desktop**: copy one folder of this repository into the folder your runtime reads skills from. Which folder goes where, with the commands: [docs/agent-setup.md](./docs/agent-setup.md#skills).
- **Role skills** (critic, security reviewer, the team's rubrics), only if you want those roles: `make install-skills`, from a clone, on macOS and Linux, installs every skill for every runtime it finds; on Windows, copy the folders by hand.

To give the roles more tools (subagents, MCP servers, a knowledge base): [docs/customizing-roles.md](./docs/customizing-roles.md).

---

## Try it

Two terminal windows, both inside the same git repository but in **two different folders** (for example the repository root and a subfolder). Two windows in the same folder are the same agent.

**Window 1**, the orchestrator:

```
cab-bridge join --role=val --agent-name=VAL-main
cab-bridge next
```

`join` registers this window and shows who else is here. `next` waits until a message arrives, prints it and exits.

**Window 2**, the executor:

```
cab-bridge join --role=esc --agent-name=ESC-main
cab-bridge tell VAL-main "hello from window 2"
```

Window 1 prints the message. That is the whole mechanism.

### With agents

In practice you do not type these commands: your agents do. Open an agent in each window and tell it, for example:

- Window 1: *"Join the bridge as val, named VAL-main, and wait for messages."*
- Window 2: *"Join the bridge as esc, named ESC-main. VAL-main will send you work."*

From there they talk on their own. Claude Code is woken when a message arrives. Codex CLI cannot be woken from outside by itself: it gives its thread id to the orchestrator, which wakes it with `codex queue`.

### Roles

`val` (orchestrator), `esc` (executor), `critic`, `observer` (reads, cannot send), or any name you choose: two equal agents can both be `peer`. Executors do not message each other directly; they go through the orchestrator, unless you allow it with `--allow-mesh`.

---

## Everyday commands

```
cab-bridge join --role=<role>     Register this window (safe to repeat) and see who is here
cab-bridge next                   Wait for the next message and show it
cab-bridge ask <agent> "..."      Ask something: stays open until they reply
cab-bridge tell <agent> "..."     Inform: no reply expected
cab-bridge reply "..."            Answer whoever asked you

cab-bridge peers                  Who is in this project
cab-bridge overview               Me, my peers and my inbox at a glance
cab-bridge cleanup                Remove this window's session
```

A long message can come from a file: `cab-bridge tell VAL-main < report.md`. Every command explains itself with `--help`; the complete list is in [docs/agent-setup.md](./docs/agent-setup.md#all-commands).

## Claude Desktop

On macOS and Linux, Claude Desktop (including Cowork) can join a project's conversation through `cab-bridge mcp`, opened per project by the orchestrator with `cab-bridge desktop open`. Not available on Windows. Setup: [docs/claude-desktop.md](./docs/claude-desktop.md).

## Your data

Messages are files under `~/.claude/cli-agents-bridge/` (on Windows, `%USERPROFILE%\.claude\cli-agents-bridge`). On macOS and Linux they are created readable only by you. On Windows they take the permissions of your user folder, which by default admit you, SYSTEM and Administrators. Threat model and controls: [SECURITY.md](./SECURITY.md). Data flows and GDPR: [PRIVACY.md](./PRIVACY.md).

---

## For agents

You were asked to install or use this bridge. Read [docs/agent-setup.md](./docs/agent-setup.md): it has the install commands with checksum verification for each system, the skill to load for your runtime, a smoke test that does not touch the person's real sessions, the protocol in short, and what not to do without the person's yes.

For a person: copy the prompt at the top of that page into your agent, and it does the setup and tells you what it checked.

---

## Versions

- **v0.11.0** (current): Claude Desktop joins the bridge, through `cab-bridge mcp`.

`main` is ahead of the latest release: the changes, including fixes that matter on Windows, are under Unreleased in [CHANGELOG.md](./CHANGELOG.md), with every earlier release. To use `main` now, build from source with Go 1.25 or later: `make build` on macOS and Linux puts the binary in `bin/`; on Windows, `go build -o bin\cab-bridge.exe ./cmd/cab-bridge`.

## Contributing

Go style, commit format and test patterns: [docs/dev-conventions.md](./docs/dev-conventions.md). Design rationale: [PLAN.md](./PLAN.md).

## Authors & credits

- **Idea, direction & review**: [Alan Curtis](https://www.alancurtisagency.com), AC Agency
- **Implementation**: Claude (Anthropic), from Opus 4.7 to Opus 5.5 and Sonnet 5.5

Built as a team of agents on this same bridge: Alan drove the vision and approved every plan and merge; Claude designed and wrote the code. *The idea is Alan's, the code is Claude's.*

The basic idea, two Claude Code sessions talking through files, came from Shreyas Patil's [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) (MIT). cli-agents-bridge is a new codebase, written in Go, with its own design, and keeps its data apart from that plugin's (`~/.claude/session-bridge/`).

## License

MIT, see [LICENSE](./LICENSE).
