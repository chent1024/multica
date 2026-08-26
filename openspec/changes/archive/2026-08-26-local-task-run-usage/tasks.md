## 1. Persistence and Local Interfaces

- [x] 1.1 Add the version-9 `task_run_usage` schema and forward migration with composite primary key and non-negative constraints.
- [x] 1.2 Implement transactional complete-snapshot replacement, validation, fixed-local-space ownership checks, empty clearing, and explicit Issue cleanup.
- [x] 1.3 Hydrate task-runs and single-Run responses with per-model usage and implement the existing Issue usage summary response.
- [x] 1.4 Add backend tests for migration reopen, multi-model storage, idempotent repeat, replacement, empty clear, invalid rollback, local-space isolation, terminal statuses, summaries, and Issue deletion.

## 2. Daemon Delivery

- [x] 2.1 Make empty usage snapshots reportable and add a dedicated short transient retry schedule using the existing retry classifier.
- [x] 2.2 Add daemon client and execution-path tests proving transient retry, permanent-error stop, and usage delivery for completed, failed, and cancelled results before terminal callbacks.

## 3. Local Issue Usage UI

- [x] 3.1 Add weighted cache-hit and cumulative-duration derivation without changing shared runtime pricing behavior.
- [x] 3.2 Replace Issue cost, currency cache-savings, and per-agent spend presentation with local token KPIs and per-Run status, stage, model, token, hit-rate, and duration details.
- [x] 3.3 Update all supported locale resources and focused component/utility tests for weighted aggregation, zero-denominator unknown, historical unknown usage, accessibility, and absence of currency UI.

## 4. Validation and Delivery

- [x] 4.1 Run focused Go, core, and views tests plus TypeScript typecheck and `openspec validate local-task-run-usage --strict`.
- [x] 4.2 Run the highest stable local UI/browser acceptance available for the Issue execution-log usage path, reporting any blocked visual layer as unknown.
- [x] 4.3 Run `make check`, perform one final `rv`, fix only in-scope findings, and rerun affected verification.
- [x] 4.4 Synchronize completed tasks, archive the OpenSpec change in the same candidate, commit with the repository convention, and freeze the local branch without push or deploy.
