# Review checklist

The defects this repository has had more than once, each with the trigger that makes it visible in a diff and the check to run. Written for whoever reviews a plan or a diff here, human or agent.

## How to use it

- **Read it from the default branch** (`git show main:REVIEW.md`), never from the branch under review: a check the change can edit checks nothing.
- **Apply only the items whose trigger appears in the diff.** Usually a handful do. In the verdict, list the items that triggered and the outcome of each: `holds`, or the finding it produced.
- **Each item names where it comes from**: findings in `ROADMAP.md` (`F-nnn`, `BUG-x`) and sections of `docs/dev-conventions.md`. An item enters after the case happened at least twice and can be seen from a diff; it leaves when a test, a lint or a guard covers it.
- **Already enforced, do not report**: gofmt, `go vet` and staticcheck for darwin, linux and windows, the test matrix on three systems (`.github/workflows/ci.yml`), action pinning and job permissions (`tests/integration/workflow_standards_test.go`), the single retry loop (`internal/security/retry_single_loop_test.go`), the Unix binary importing only the standard library (`tests/integration/unix_deps_test.go`). Report only what they miss.

## Tests

**1. A test that does not exercise what it claims.** F-93, F-102, F-124, F-129, F-143, F-148, F-176, F-177; `docs/dev-conventions.md`, "A test's premise is asserted, never skipped".
- Trigger: a new or changed test, above all one that asserts a limit, a guard, or an exclusion between processes.
- Check: it is red on the code without the fix, or on a mutant that undoes it; its premise is asserted, not assumed; an exclusion between processes runs with real processes, not goroutines of one process.

**2. Time or a race inside a test's oracle.** F-119, F-130, F-138, F-153, F-163, F-175; "A test that bets on winning a race".
- Trigger: a timeout or a threshold inside an assertion, `time.Now()` in an exact comparison, a loop that repeats until it wins, a snapshot of the disk while a goroutine writes.
- Check: a timeout is patience, not the oracle; the interleaving is forced through a seam; a probability is measured, not assumed.

**3. A test that reads source files, workflows or the Makefile.** F-139, F-152, F-163, F-164, F-184, F-185; "A test that reads a source file brings it to `\n` first".
- Trigger: a test that reads, parses or edits a tracked file, or an inventory written by hand.
- Check: line endings normalized before matching; the premise of an edit asserted; a self-test that shows it can be red; what it cannot see is written in it; a new element enters the inventory by itself.

**4. A test that touches the real environment or copies the wrong tree.** F-100, F-129, F-131, F-149, F-158.
- Trigger: a test that runs the binary, `git`, `go` or `make`, sets `HOME` or `PATH`, or copies the repository into a fixture.
- Check: data dir, `HOME`, `PATH`, the global git config and the module cache all belong to the test; reaching the real data dir is impossible, not "tolerated if it fails"; the copy holds the files the test means to prove (uncommitted ones included, when it proves an uncommitted fix).

**5. Fake tools on the `PATH`.** F-129, F-132, F-159.
- Trigger: a fake `go`, `staticcheck` or other tool put on the `PATH` by a test.
- Check: the test asserts that the fake answered (`FAKE_TOOL_LOG`, `tests/integration/fake_tools_test.go`); the fake runs on Windows (compiled, not a script without extension); it prints every output form the real tool has, old and new.

**6. A change that edits its own judge.** F-139, F-164.
- Trigger: the diff changes a test, a guard, a workflow or a contract fixture that judges this same change (for example `tests/integration/testdata/release.yml.contract`).
- Check: always reported, with the line and the reason the change gives. An existing test is never changed to make it pass.

## Shared state and its siblings

**7. The sibling left behind.** F-30, F-51, F-112, F-147, F-166, F-172, F-175, F-177, F-183.
- Trigger: the diff changes one of several readers, writers or deliverers of the same thing: the readers of `inbox/` (`readMailbox`, `collectInbox`, `countUnread`, `unreadFromPeer`), the deliveries (`send`, `deliverResponse`, the MCP server), the `notified` and `replayed` states.
- Check: list the other callers, the other exit paths and the `else` branch with `rg`; each gets the same treatment or a written reason.

**8. The readers of a field that changes meaning or loses its writer.** F-99, F-102, F-112, F-124, F-151, F-166, F-167.
- Trigger: how a field of the manifest, of a message or of the reply journal is written or derived changes, or its writer is removed.
- Check: every reader, found with `rg`, holds with the new meaning; a field nobody writes has no readers left. `tests/integration/manifest_writers_test.go` limits who writes the manifest, not what the fields mean.

**9. A second source for a decision that already has one.** F-112, F-128, F-139, F-152, F-172, F-184.
- Trigger: the diff computes again a value or a decision that already has a function or a constant (listening, a budget, a retry loop, a version number).
- Check: change the value at the source and look at what stays behind.

**10. Check and act outside the session lock.** F-93, F-102, F-160, F-176, F-177, F-178; "Check and act in one critical section".
- Trigger: a rename, remove or write in `inbox/`, `processed/`, `outbox/`, the manifest or the reply journal outside `WithSessionLock`, or split across two sections.
- Check: the check and the action are in the same closure; a writer checks that it still owns what it writes; no path holds two session locks at once. Covered in part (`manifest_writers_test.go`, `internal/session/replytxn_section_test.go`); the moves in `cmd/cab-bridge` (`inbox --tidy`, `ack`, the copy into `outbox/`) have no structural guard.

**11. Deleting or moving messages.** F-11, F-30, F-104, F-115, F-170, F-171, F-176; "A delete that retries is blind".
- Trigger: `os.Remove`, `os.RemoveAll`, `security.RemoveFile`, an archive or a sweep.
- Check: archive before delete, and the first error stops the whole operation; only what was shown, and is not an open ask, is moved; the command says which data dir it acts on; a blind retry by name is acceptable there (`internal/security/remove_callers_test.go` lists who may use it).

**12. The on-disk format and the previous binary.** F-124, F-160, F-168, F-177.
- Trigger: a change to the schema of the manifest, a message, the reply journal or the Desktop grant, to a name under `layout-2/`, or to the lock protocol.
- Check: files written by the previous version load, or are refused with an explicit error; a process of the previous version still running (an MCP server started before the upgrade) cannot corrupt what the new one writes.

**13. Liveness read from another object's signal.** BUG-A, F-112, F-160.
- Trigger: deciding alive, stale, orphan or listening from `IsProcessAlive` on a PID, the heartbeat's age, the lock, or the `orchestrating` state.
- Check: whose process that PID is; who refreshes that heartbeat; what happens to a peer that is idle but can be woken.

## Errors and exits

**14. An error in the wrong bucket.** F-51, F-137, F-153, F-160, F-166, F-172, F-176, F-177; "A retry loop that decides on a sentinel".
- Trigger: `errors.Is`, `os.IsNotExist`, `os.IsPermission`, `errors.Join` with a sentinel, a `syscall` errno, a branch that maps an error to an outcome or a text.
- Check: different causes give different outcomes (busy or a failed close, a permission error or a corrupt file, a guard or a timeout); the predicate sees through a wrapped error; the constant means the same on every system.

**15. An error dropped.** F-51, F-145, F-166, F-167, F-171, F-176, F-183.
- Trigger: `_ =` on a call that can fail, `continue` on a read error, `Close` ignored on an error path, a default value in place of an environment error (`os.UserHomeDir`).
- Check: every error is returned, logged, or turned into a visible outcome. The repository has no errcheck lint.

**16. Exit 0 on a refusal, or a value outside the grammar accepted.** F-105, F-107, F-113, F-145, F-165.
- Trigger: a branch that prints a refusal or a warning and returns nil; an argument accepted without validation.
- Check: a refusal exits non-zero through the central error-to-exit-code map; a value copied from a list, or with a typo, is rejected.

**17. A printed remedy that does not work.** F-91, F-107, F-126, F-140, F-173.
- Trigger: an error, a warning or a `--help` that prints a command or a flag to copy, or advice written inside a primitive shared by several commands.
- Check: run literally, from every command that prints it, it works and does not cause the defect the command avoids. `tests/integration/lock_remedy_drift_test.go` covers one remedy only.

## Text that people and models act on

**18. A comment that promises more than the code.** F-100, F-102, F-115, F-124, F-137, F-143, F-146, F-163, F-185.
- Trigger: a comment or docstring with a guarantee (every, never, sole caller, bounded, no fallback), or a behaviour change next to a comment that argues for the old behaviour.
- Check: a test executes the guarantee; when the diff reverses a decision, the comment that defended it changes in the same diff.

**19. Documents, skills and versions left behind.** F-121, F-139, F-146, F-173.
- Trigger: a command's flags, contract or refusals change, or a security control changes, and `README.md`, `SECURITY.md`, a skill, `--help` or the plugin manifests are not in the diff.
- Check: `rg` the old behaviour in the texts; every line of `SECURITY.md` this diff touches is on the live code path. Covered in part (`tests/integration/version_drift_test.go`, `tests/integration/skill_drift_test.go`).

**20. Text a model reads to act, proved only by tests.** F-72, F-120, F-121, F-173, F-174.
- Trigger: strings in the MCP results and tool descriptions (`cmd/cab-bridge/mcp_*.go`), the output of `next`, `join` or `peers`, a skill.
- Check: tried with the real client in two new conversations, or declared not tried. Tests fix the words, not how a model reads them.

## Names, paths and sizes

**21. Path identity.** BUG-5, F-41, F-135, F-142, F-143, F-157.
- Trigger: paths compared with `==`, `strings.HasPrefix` or `strings.EqualFold`, or a comparison chosen by a build constraint.
- Check: tried with different case, symlinks in both directions, a trailing separator, a Windows drive; case sensitivity belongs to the volume, not to the operating system.

**22. Derived names that collide.** F-6, F-110, F-124, F-125, F-152.
- Trigger: a name or a label derived from something else (a basename, a sanitized string, a hash, a role).
- Check: two different inputs give two names or an explicit error (the same basename in two projects, characters that sanitize to the same thing, the same role in the same folder).

**23. Paths with special characters in a shell or in Make.** F-124, F-174, F-181.
- Trigger: a path in a recipe, in `$(shell ...)`, between double quotes, or a token printed to be pasted.
- Check: probed with a space, `#`, `$`, a backtick and an apostrophe; the token arrives as one argument through a real shell.

**24. A size measured on the wrong object.** F-72, F-151, F-172.
- Trigger: a field or a text that states a size (`bytes`, `[N B]`, a budget), or a cap counted in items.
- Check: the size of what, exactly (the file, the text, the written result); a cap is counted in the bytes of the real result.

## CI and toolchain

**25. Workflows and the toolchain.** F-128, F-144, F-159, F-162, F-164, F-169, F-187.
- Trigger: a file in `.github/workflows/`, the `toolchain` line of `go.mod`, `.staticcheck-version`, a version written in a script.
- Check: mostly enforced (`workflow_standards_test.go`, `tests/integration/workflow_toolchain_check_test.go`, `tests/integration/makefile_lint_toolchain_test.go`). What they miss: a new workflow file is checked only once it is added to the list in `workflow_standards_test.go`; a version number written by hand anywhere outside `go.mod` and `.staticcheck-version`.
