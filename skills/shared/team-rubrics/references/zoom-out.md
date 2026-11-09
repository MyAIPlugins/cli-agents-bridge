# Rubric: zoom-out

Adapted from google/elephant-goldfish-model (zoom-out-standard), Apache-2.0.

It serves CRI2 (mandatory lens) and the fresh subagent that the VAL launches at the end of a wave. After a NO-GO and the fixes, the VAL asks both critics for the zoom-out in the brief of the second round: the fixes add complexity that the first review could not see.

1. **Cause, not symptom**: does the fix touch the point where the defect originates, or does it cover it where it shows?
2. **Consistency** with README, CLAUDE.md, ADRs and the conventions of the repo.
3. **YAGNI**: nothing that serves only a hypothetical case.
4. **No premature optimization**: caches, queues, sharding or parallelism only with a measured bottleneck.
5. **The minimum number of components** the data passes through.
6. **The logic in the right layer**.
7. **Reuse**: what already exists in the repo before something new.
8. **No local workarounds** when the structural fix is cleaner.

At the end of a wave the zoom-out looks at the lots as a whole: duplications, naming, contradictions between lots. What it finds becomes a new lot, without reopening the previous ones. The report goes to a file, `.handover/zoom-out-<YYYY-MM-DD>.md`.
