# Lessons: the incidents that produced the rules

Reference of `cab-bridge-awareness`. The core keeps the rule and one line of reason; here is the story, with the date. A new rule born from an incident: the rule in the core, the story here.


## Entry

Field data (9 Aug): three fresh agents spent **six commands each** where two were enough (`join`, then `overview`, then `inbox --list`, then `peers --all-scopes`, then two `--help`), looking for confirmation of things `join` had already said. *(A **pre-F-116** episode: today `peers --all-scopes` is no longer redundant; it is the only way to see the other repositories, which `join` does not show. The other four remain duplicates.)*


## Mental model: why reply closes a single delivery

- **Only `reply` archives, and it archives ONE delivery** (F-109, 10 August): it closes the asks that sender sent you **in a single page of `next`**, not everything they have open. What arrived later stays open, is **named under your reply**, and **goes back in the queue**: the next `next` redelivers it to you marked `redelivered`. Confirmation remains a side effect of the work, never a ritual, but the work you confirm is the delivery that was shown to you.
  *Why*: `NOTIFIED` means **"the `next` process printed it"**, not "you read it". You re-arm before working (correct), and a message that arrives **while you are writing** is `NOTIFIED` without you having seen it. Before, your reply closed it anyway, and a *"stop, do NOT do A"* could end up answered by a *"did A as asked"*. A real incident, not a hypothesis.
  *Declared limit*: without a read ACK it is not impossible by construction: the oldest page may be exactly the one you have not read. What is guaranteed is **at most one delivery per reply** and **never silently**: you see what you closed and what remains, the sender sees `closes` on the reply and `requeued` in `sent`.
  **So read the lines under your reply.** If they say something was left open, it is not an error: it is a message you had not seen yet, and it is coming back.


## Sending method

**And it applies to EVERY command that takes text, not just the bridge.** The case that got me after I had written this rule: `git commit -m "... ~0.6ms on `+'`peers`'+` with 4 sessions ..."`. The shell executed `peers` as a command and the message ended up in the history with a hole in its place, already pushed and not rewritable. **For commit messages: always `git commit -F <file>`** (written with `Write`), or a heredoc with a quoted delimiter. The risk does not belong to the bridge: it belongs to any text that passes through a shell line.

**Why "be careful" is not enough.** The shell interprets backticks, `$` and quotes **before** the binary is invoked: when `cab-bridge` starts the content is already corrupted, and **no tool-side check can notice** (there is no original to compare it with). The command exits successfully and the message leaves in pieces.

**It does not depend on length.** One backtick in three lines is enough. The correlation is with the CONTENT (markdown, file names, snippets), i.e. everything a technical agent writes.

**The data that produced the rule (8 Aug):** six messages sent mangled or empty in a single session, all from the VAL, all while it was coordinating the arc that eliminates that defect. One with EMPTY content, delivered normally; in another a backtick invoked `join`, the Unix command. The VAL had known since the morning and fell for it again four times. **Discipline did not work: only removing the choice works.**


## Authority

The recurring defect: the VAL sees the task it ASSIGNED come to an end and concludes that the work is finished, but only the human knows the complete plan, and almost always the next steps exist and have not been communicated yet. **"They have finished the task I gave them" does NOT mean "they have finished".**

Real error (8 Aug): to the question "when can I deactivate the CRIs?" the VAL answered "now, both of them". They were needed right afterwards and produced 2 P0s and 7 findings, including defects the green gate could not see.


## Re-arming and CAB_SESSION_ID

The orchestrator's most frequent defect is not technical: **it forgets to start listening again** and loses messages without noticing. ESC and the critics do not have it, and not through merit: their loop is closed (receive → work → reply → re-arm) and re-arming is the obvious last action of a single sequence. The VAL, between one message and the next, reads, decides, updates docs, commits, writes to multiple agents: re-arming becomes ONE of the N things. **It is a defect in the structure of the role, not in attention.**

**And even before that: pin `CAB_SESSION_ID`, always.** The VAL's work requires entering other agents' directories (reading a worktree's `git log`, inspecting a critic's repo) and **the shell's cwd persists from one call to the next**. From that moment every id-free command is no longer yours: it resolves the session of whoever lives there, **succeeding**, with no errors and no warnings.

Real error (8 Aug, twice in the same evening): a `cd` into ESC's worktree for a `git log`, and the next three "re-arms" armed a `next` on **ESC's session**, stealing its ownership of the wait, while I believed I was listening on mine. The first time I had even declared a healthy feature broken because I was looking for my messages in someone else's inbox.

**And do not truncate the messages you read.** If you use a script to extract the `content` from the `next` payload, do not put a character limit on it: a long verdict arrives half-way and you act on half. Happened on 9 Aug: four findings of a NO-GO, I read two and forwarded those to the executor as if they were all of them; the two lost ones contained the best. **For a long message use `cab-bridge read <msg-id>`**, which prints everything.

**And do not pipe `next` into `tail` or into a pipe that buffers.** Real error (8 Aug): a background `next` with `| tail -60` held back two reports for twenty minutes (`tail` emits nothing until it receives EOF). The VAL read an empty file and concluded that a critic had not worked. Redirect directly, without filters.


## Ground truth

It applies especially **to the VAL**: in a single evening I declared a healthy component broken, got two counts wrong in the same account, and concluded that a critic had not worked while its report had already been on my disk for twenty minutes. **The disk beats the account even when the account belongs to whoever holds the gate.**
Corollary for plans: a claim about the code's behavior inside a plan ("X holds the lock") must be **verified on the code** before ratifying it, never praised on trust.

**And the flip side, which is just as seductive: the RED that gets explained away.** A failure that disappears on the second attempt **does not say "environment"**: it says *non-deterministic*, and non-determinism, seen from outside, is exactly what a race looks like. The sentence *"a red that disappears when the environment changes is environment, not code"* would classify as contention a defect that shows up only under load, i.e. the most expensive category to find later. **What tells them apart is in the log, and looking costs nothing**: real contention = the process *was making progress* and was killed by the cap; real defect = dead, or hanging **without making progress**. After two days spent on *"green that proves nothing"*, this is the same structure reversed: the explanation is plausible, so it does not get verified.


## The four questions, in the long form

Born from ten errors of mine in one day. They are not good intentions: they are four questions with an answer, to ask yourself **before** pressing enter on a brief, a document or a message to the human.

**1. Did I EXECUTE it or DEDUCE it?** And say so explicitly, always, even when the deduction is good. I wrote twice that a residual security risk was "narrow", with a correct argument (*SC-7 keeps the base at 0700, so the tree is frozen*) that answered **a different question**: it said nothing about a link planted BEFORE, i.e. the exact window the check existed for. Reproduced in thirty seconds by a reviewer. **A plausible argument is not a verification, and the difference only shows by trying.** If you did not execute it, write that you did not execute it.

**2. WHICH OBJECT is this data a property of?** The number can be true, clean and repeatable, and concern the wrong thing. Three times in one day: 525 ms was the latency of the **process** and I attributed it to the **model**; "the terminal dies after an hour" was true on **one version** and I took it as a property of the runtime; "the cadence emerges on its own" was **the 10 minutes the human had instructed**, inside that agent's goal. Common signature: **I observe a configured system and infer its default from it.** Double-checking does not catch this error, because the data holds up under every check.

**3. Did I PRODUCE this state?** Do not describe to the human a world you have not created. I said "the CRI diff-gate remains" without having requested it: the critic polled in vain for half an hour, and had no way to distinguish *"the VAL hasn't finished"* from *"the VAL has forgotten me"*. In the same family: **gated is not merged, merged is not installed.** The binary in PATH is the only thing the agents actually use.

**4. Am I describing the CODE or the DESIGN?** When you write a normative document **you** become the source someone else will trust instead of the code. I declared a security check "active" while it was on a branch and with a behavior that not even there was the one described, an hour after rewriting that very file because it contained claims not supported by the code. **Operating rule: the document is updated AFTER the merge, never before.** Code and documentation land together.


## Destructive tests

It is not enough to say *"verify on the code"*. An agent testing commands that delete will do it on the machine it is on, and **a `cd` into a test directory does not protect it**: the `cab-bridge` data dir derives from `$HOME`, not from the cwd, and back then no output said so (today, 0.11.0, `cleanup --scope=global` prints it and requires `--data-dir`).

Real cost (9 Aug): a critic testing an attack vector on `archive/` ran `cleanup --scope=global --force` without `CAB_DATA_DIR`, trusting its own `cd`. **Thirteen archived sessions deleted from the production data dir.** Recovered from a snapshot, but by luck, not by design. The critic had **predicted that defect a few hours earlier** and fell into it anyway: knowing is not enough, when the command does not remind you at the moment it matters.

**So in the brief, always, in one line:** *"for any test that deletes or modifies, export `CAB_DATA_DIR` to a sandbox: the cwd isolates nothing"*. And if the command has a `--dry-run`, name it.

It is a mitigation, not the fix: the fix lies in the tool, which must say what it acts on before acting (for `cleanup`, 0.11.0 does). But the line in the brief costs one sentence and covers the agent that has not read the skill.


## Close the loop

An agent that sent you a verdict **stays waiting for the outcome**, and has no way of knowing whether you are working or have forgotten it. Twice in one day I consumed a critic's work (five findings, a reproduced P0) and did not answer it: from its side it was silence identical to oblivion.

When a finding is closed, **tell it what happened to each one**: what you verified, what you merged, what you discarded and why. It costs one message and makes the next round useful; the alternative is a critic that asks the same questions again or stops asking them.


## And the branch next to it

The countermeasure to the most expensive defect of the v0.8 arc. When a pre-validated synthesis gave way, the branch that held was **always the one named in the finding**, and what gave way was its complement: same-role verified / cross-role not · `notified` pruned / `replayed` not · `buildOverview` migrated / the renderer not · the pass that emits covered / the one that gets interrupted not.

It is not that the verification was superficial: **you verify the case being talked about, and nobody names the complement.**

Form with an answer (otherwise it becomes a ritual): *does this fix touch a field, a path or a branch? then: who else reads that field · what are the other exit paths · what is in the `else`*.


## Roles

**A `critic` talks ONLY to its own VAL**: never to the executor, never to other critics, never to an `architect`. It is an invariant of the role, not a convenience: independence is the value a critic brings, and two critics who compare notes converge into a single voice; and a critic who writes directly to the executor bypasses the VAL's verification, which is the point where a finding is checked before it becomes work. **If a critic has something for another agent, it tells the VAL and the VAL forwards it**, and that step is not bureaucracy: it is where the finding gets verified.


*Error of 9 Aug: I had written `architect` for the critics in all three skills. The right role for a critic is `critic`; `architect` belongs to Claude Desktop.*

## F-135 and merged is not installed (Windows, 12 September)

🟢 **F-135 HAS BEEN CLOSED since the afternoon of 12 Sep: `fromAddressShellArg` verbatim works on Windows too,
from binary `v0.9.0-28-g9298628`.** `[E]` Verified in both directions: the bridge's VAL sent a
message **with the path** and I sent one back **with the path**, both arrived.
⇒ **The workaround below is NO LONGER needed**, and it is kept only so that the error's signature stays
recognizable if one day someone works on an older binary.
⛔ **Before applying it, check the version**: `cab-bridge version`. Below `-28-g9298628` it applies; above
it does not.

<details>
<summary>The signature of the defect, for whoever runs an earlier binary</summary>

🔴 ⛔ **ON WINDOWS `fromAddressShellArg` DID NOT WORK: the project NAME was used instead** (12 Sep, F-135).
`[E]` `next` delivers as the sender's address the **absolute path**
(`'VAL-bridge@C:\<path>\cli-agents-bridge'`). Used **verbatim, as this skill prescribes**, the
`tell` fails:

    no agent named "VAL-bridge" in project "C:\<path>\cli-agents-bridge" ... projects with agents: other-project, **cli-agents-bridge**

(one line in the real output, shortened here)

⇒ The binary **lists among the valid projects the very one it has just rejected**: that is the signature.
`[L]` The cause (from the bridge's VAL): `recipient.go:72` decides *"is it an absolute path?"* with
`strings.HasPrefix(hint, "/")` ⇒ on Windows `C:\…` **does not start with `/`**, ends up in the basename
branch, and `Base()` will never match the full path.

🟢 **Until the binary is updated, use `<name>@<PROJECT-name>`**: the `SCOPE` column of
`peers --all-scopes`, not the path.
⚠️ **It is not universal**: the bare name is a basename, so with two repos with the same final folder
(`…/a/app` and `…/b/app`) it becomes ambiguous, and the way out intended for that case is
**precisely the path**, i.e. the broken branch. Today it does not bite because the basenames in use are distinct.
📌 On Unix the original line remains **true**: the `HasPrefix("/")` branch works. It is a
Windows-only defect.
📌 Fix in `feat/windows-lotto1b`, **merged and installed on 12 Sep** (`v0.9.0-28-g9298628`).

</details>

⭐ **And the lesson that remains, when the defect is gone**: between the `[E]` of a report and the
binary the agents actually use there are **push → CI → merge → REINSTALL**, and each of the four
can stall. `[E]` Here the fix had been written **for nine days** and was stuck: what brought it into PATH was a
real case that ran into it, not its correctness.
⇒ **Merged is not installed**, and the only thing that counts is `cab-bridge version`.
