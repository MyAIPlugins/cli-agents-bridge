---
name: team-rubrics
description: "Rubrics and common rules of the agent team (VAL, ESC, CRI, CRI2, BRO, ARC, SEC). Plan \"what and why\", detail plan, zoom-out, naive reader, severity Blocker/Major/Nit, triage of findings, the \"don't know\" vocabulary, the block schema. Load it when you write or review a plan, a diff, a verdict or a report."
metadata:
  author: alan-curtis
  version: "1.0.0"
---

# Team rubrics

References loaded when they are needed, not to be read all of them every time. They apply the same on Claude Code and on Codex.

| file | who uses it | when |
|---|---|---|
| `references/common-rules.md` | everyone | always, when you deliver or judge: terms, markers, the "don't know" vocabulary, severity, triage, blocks, briefs to subagents |
| `references/plan-what-and-why.md` | ESC (part A of the plan), ARC (proposals), CRI2, SEC | plan of a lot with a plan gate; architectural proposals |
| `references/plan-detail.md` | ESC (part B), CRI (mandatory lens) | every plan; and the diff compared with the plan |
| `references/zoom-out.md` | CRI2; CRI in the round after a NO-GO; end-of-wave subagent | second review after a NO-GO; end of wave |
| `references/naive-reader.md` | CRI2 (mandatory lens), whoever reviews ARC's proposals | every plan or proposal |

The rubrics are adapted from **google/elephant-goldfish-model** (Apache-2.0); the "don't know" vocabulary, the block schema and the counterargument written last from **markfulton/ai-employees** (MIT, rules rewritten, no code).
