# Rubric: detail plan (part B)

Adapted from google/elephant-goldfish-model (implementation-readiness), Apache-2.0.

It serves the ESC for part B of the plan (in every lot), and CRI as a mandatory lens: is the plan ready to implement?

1. **Zero decisions left open**: the plan leaves the implementer no design choice (data schema, path of an API, dependency, handling of a new edge case). "Choose the library for the queues" in a plan is a Blocker.
2. **Shape of the data before the code**: types, schemas, fields, with who writes them and who reads them.
3. **Incremental phases**: each one leaves the code compilable and testable, in order.
4. **Real names**: files, functions and symbols that really exist in the repo (whoever critiques looks them up), or declared as new.
5. **Test seams**: where each phase is tested and what each test catches. No tautological tests or tests coupled to the implementation.
6. **Map from BAR criterion to test**: every BAR criterion has the test or the gate that verifies it; a criterion without a test is a finding.
7. **ADR**: the IDs of the applicable ADRs, and no choice that contradicts an Accepted one.

On the diff, the same lens becomes **adherence to the plan**: each phase done, changed (with the reason) or skipped (with the reason).
