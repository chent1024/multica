## 1. Persisted ownership contract

- [x] 1.1 Add fresh-schema and forward-upgrade handling for mandatory project ownership and unambiguous legacy backfill.
- [x] 1.2 Enforce required, immutable project ownership and parent-project inheritance in Issue creation and update transactions.
- [x] 1.3 Reject Run creation/continuation for projectless legacy Issues and protect project deletion when tasks remain.
- [x] 1.4 Add Go regression tests for missing/unknown projects, child inheritance and mismatch, one-time legacy assignment, migration, execution rejection, and project deletion conflicts.

## 2. Client contract and creation experience

- [x] 2.1 Require project ownership in local task-create API behavior and the Codex quick-create client while preserving the bounded child inheritance call shape.
- [x] 2.2 Add required-project behavior to the shared picker and make assigned Issue ownership read-only while permitting legacy repair.
- [x] 2.3 Update manual and Codex creation to honor caller context, auto-select the only project, require an explicit choice among several, and guide zero-project users to add a project.
- [x] 2.4 Remove visible workspace context from local navigation and task creation while keeping Tasks as the cross-project aggregate.
- [x] 2.5 Add or update Vitest coverage for both creation modes, picker locking/repair, empty project state, and local sidebar navigation.

## 3. Verification and documentation

- [x] 3.1 Update current repository documentation where task/project ownership or visible workspace behavior is described inaccurately.
- [x] 3.2 Validate the OpenSpec change and run formatting, affected Go tests, affected Vitest suites, and TypeScript typecheck.
- [x] 3.3 Run one bounded `rv`, fix in-scope findings, and rerun only affected verification plus the highest-level local observable check.
