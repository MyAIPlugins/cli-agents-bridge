# Common rules of the team

## Terms

| term | meaning |
|---|---|
| VAL | the evaluator: orchestrates the team, writes the BAR and the documents, holds the gates, triages the findings |
| ESC, ESC2 | executors: they write the code; ESC2 is a second executor on an independent lot |
| CRI, CRI2 | independent critics; in the team these rubrics come from, CRI runs on Codex and CRI2 on Claude Code |
| SEC | security reviewer (skill `security-expert`), on any runtime. It is activated when the BAR says "SEC: yes" |
| BRO | browser verifier |
| ARC | the architect, Claude Desktop: talks only with the VAL |
| lot | an assigned unit of work, with its BAR, that ends in a merge |
| wave | a group of lots decided together; at the end of the wave the VAL does the zoom-out |
| BAR | the acceptance criteria of the lot, written by the VAL before assigning it |
| gate | a check that decides whether the lot passes: deterministic if it is a command with an outcome (test, build, measurement), not an opinion |
| plan gate / diff gate | the critics' review of the plan before the code / of the diff after it |
| round | one full pass of a gate: review, triage, fixes; the next round reads the fixes again |
| lens | the point of view from which a critic reads a plan or a diff, that is one of the rubrics; "mandatory lens" means that critic always applies it |
| needs_validation | a plausible hypothesis of SEC that lacks a fact, which SEC names; it has no severity (skill `security-expert`) |
| ADR | Architecture Decision Record, in `docs/decisions/` of the repo when there is one; `Accepted` binds |
| seam | the point in the code where a test can observe the behaviour without depending on the implementation |

## Evidence markers

- `[E]` executed: you ran or measured it yourself, now. Reserved for this.
- `[L]` read: you read it in the code or in a document, without running it.
- `[D]` deduced: reasoning, not verified. Say what it would take to verify it.
- `[J]` opaque judgment of another tool (for example a browser agent that says "done"): the real measurement goes under it.

## The "don't know" vocabulary

When something has not been verified, one of these states is used, **never an estimate**:

| state | means | never write |
|---|---|---|
| `not run (<reason>)` | test, gate or command not run | "passes" |
| `not read` | file, page or log not opened | "no change", "no problem" |
| `never measured` | metric or effect not instrumented | "it worked", "probably ok" |
| `no baseline` | first measurement, no comparison possible | "it improved" |
| `partial (<what is missing>)` | incomplete result, delivered anyway | "done" |

A GO or a "done" with criteria in one of these states declares it **at the top**, not at the bottom.

## Severity (the same for every critic)

- **Blocker**: the lot does not pass. Equivalent to NO-GO.
- **Major**: fixed before the merge. If it is not fixed, the VAL brings it to the human with ESCALATE and the written reason: it is a risk the human accepts.
- **Nit**: recorded, does not block.

SEC assigns the team severity to its own confirmed findings by itself, with one line of reason for each (impact in that context); needs_validation and notes stay out-of-scale. A single fixed rule: a secret in the diff is always a Blocker. SEC's "GO with fixes" verdict counts as a GO with open Majors: they are closed before the merge. The VAL does not change a SEC severity, neither downward nor upward: it contests it with the evidence (CONTEST), and if the disagreement remains the human decides (ESCALATE).

## Proof of a finding

A finding counts only **with its proof**, in one of two forms:
- **on the diff or on the code**: command, output obtained, output that the BAR or the intent require (`[E]`);
- **on a plan or a design**, where there is nothing to run: the section or the line cited, what it says and why it does not hold, and what it would take to prove it once the code is written (`[L]` if you read it, `[D]` if it is reasoning).

Both are valid proofs for triage.

## Triage of findings (only the VAL does it)

For each finding the VAL writes a decision:
- **PROPOSE FIX**: valid; the VAL states the fix and sends it to the ESC.
- **CONTEST**: wrong or born from a misunderstanding; the VAL writes the reason with the evidence.
- **DECLINE**: only for Nits; recorded, not fixed.
- **ESCALATE**: the human decides (product, cost, accepted risk).

The VAL **does not change the severity** assigned by a critic: if it considers it wrong, it uses CONTEST. A finding with neither of the two forms of proof does not go to the ESC: the VAL asks for the proof or contests it.

## Blocks

Before declaring yourself blocked **retry the authorized path**: the block may have expired. If it remains, write:

| field | content |
|---|---|
| blocked step | what you cannot do |
| type | permission, access, input, evidence, dependency |
| who unblocks it | VAL, the human, another agent |
| evidence | command and output, or file:line |
| verified at | date and time of the verification |
| next action | what you do as soon as it is unblocked |

Same block unchanged twice: a new recovery attempt, a smaller independent delivery, or **one single precise question** to the human through the VAL. A blocked step does not stop the independent steps: the rest of the authorized work goes on.

## Interrupted remote actions

A push, a PR, a merge, a deploy or the creation of a cloud resource interrupted halfway are **read again** (remote state) before repeating them. If the effect cannot be established, that step stays still and you go on with the independent work. Avoid duplicate PRs, releases or resources.

## One writer per file

Each agent writes only its own files. No agent modifies the files of another.

## Brief to a subagent (for whoever launches them)

One level only: whoever launches the subagents is a member of the team; the subagents do not delegate. For several perimeters, several instances in the same message, each one with its own brief. The brief, always in this form:
- **Goal**: one line.
- **Where**: absolute path of the worktree, or URL or package.
- **Constraints**: what it must not do. Do not delegate to other subagents.
- **Long log**: path where to save it, or "none".
- **Return**: at most 15 lines, each claim marked `[E]`, `[L]` or `[D]` (a `[J]` only with the real measurement under it).

A longer return, or one without markers, is sent back: it is not read in full.

**Never in parallel**: two instances on the same worktree, two agents on the same browser MCP.
