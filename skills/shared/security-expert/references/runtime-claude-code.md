# SEC on Claude Code

Use it for a repository that the human has not cleared to go to the vendor of the Codex model, or to compare two SECs on the same diff.

## What you have in addition to Codex

| when | what | expected return |
|---|---|---|
| step 3, blast radius of a touched function | Serena's language-server tools (`find_referencing_symbols`, `find_symbol`), if your setup has them | callers with `file:line`; on an untrusted TypeScript repo the language server may not start: then `rg`, and you say so |
| step 5, adversarial verification | a fresh read-only `Explore` subagent to which you pass the rewritten finding, the paths and the contradictions to look for, never your conclusion | CONFIRMED / DISPROVED / DOWNGRADED with `file:line`, at most 15 lines |
| GDPR, trackers, third-party requests of a public page | a privacy-audit subagent, if your setup has one (it uses the shared browser: never in parallel with other browser agents) | requests before consent with the destination URL, cookies set, evidence |
| advisories of a new dependency | a subagent that looks up a dependency's releases and advisories, if your setup has one | version, date, advisory, URL |

One level only: you launch the subagents and they do not delegate. The brief to a subagent has the fixed form of team-rubrics (`references/common-rules.md`, "Brief to a subagent").

## What does not change

Channel, method, evidence bar, verdict and data limits are those of `SKILL.md`. The boundary of proofs is the same too: the trace on the code plus, at most, a local test in the sandbox that shows the missing check; an exploit or a reproduction against a running service is not built, and a refusal of the model at that step is the boundary, not a false positive: you tell the VAL and stop there. On Claude Code too the diff goes to the model's vendor (Anthropic): on a repository that belongs to a third party you still need the human's ok in the brief.
