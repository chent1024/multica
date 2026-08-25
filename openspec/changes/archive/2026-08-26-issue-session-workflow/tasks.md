## 1. Session Data Model

- [x] 1.1 Add Issue Session, stage occurrence, and child handoff tables plus additive task-run continuity columns to the canonical SQLite schema.
- [x] 1.2 Add a transactional forward upgrade that backfills one session and migrated implementation stage per existing issue and associates historical runs.
- [x] 1.3 Add Store types and a deep session interface for atomic session creation, deletion cleanup, reads, stage activation, handoff validation, and run binding.
- [x] 1.4 Add migration and Store behavior tests for new, upgraded, repeated-stage, idempotent-stage, invalid-handoff, and deletion cases.

## 2. Local API and Execution Continuity

- [x] 2.1 Add bounded session read and stage-activation routes with stable validation and error responses.
- [x] 2.2 Extend issue creation to atomically persist an optional structured child handoff with verified parent/session/stage/run provenance.
- [x] 2.3 Bind queued runs to the active session stage and record their predecessor and initial continuity state.
- [x] 2.4 Return prior provider session/work-directory pointers and inherited handoff context in daemon claims.
- [x] 2.5 Persist daemon-reported work directories and classify fresh, resumed, and gap continuity states from the actual provider thread identity.
- [x] 2.6 Extend task-run responses and add HTTP/daemon tests covering stage binding, continuation, replacement threads, and handoff context.

## 3. Shared Client Contract

- [x] 3.1 Add typed Issue Session, stage, continuity, and handoff schemas/types to `packages/core`.
- [x] 3.2 Add React Query options and mutations for session reads and stage activation with bounded optimistic updates and rollback.
- [x] 3.3 Extend issue-create and task-run client contracts for structured handoff and session metadata.
- [x] 3.4 Add schema, query-key, mutation, and client request tests for the new contracts.

## 4. Issue Detail Workflow

- [x] 4.1 Add a compact Session Workflow section to the existing issue-detail sidebar with loading, error, current-stage, history, and continuity states.
- [x] 4.2 Add a stage selector that appends occurrences, handles repeated stages, and states that commit/push/release selection has no external side effect.
- [x] 4.3 Group or label execution-log rows by their persisted stage and continuity state without replacing the existing transcript surface.
- [x] 4.4 Display immutable inherited handoff sections and parent navigation on child issues.
- [x] 4.5 Extend the child-issue creation flow with bounded handoff fields shown only when a parent is selected.
- [x] 4.6 Add React behavior tests for stage transitions, rollback, continuity display, handoff display, and child handoff submission.

## 5. Verification and Delivery

- [x] 5.1 Run focused Go tests, frontend tests, typecheck, and formatting on the final implementation.
- [x] 5.2 Run the highest stable local desktop user-path smoke available and report any unverified browser layer explicitly.
- [x] 5.3 Run `openspec validate issue-session-workflow --strict` and update every completed task checkbox.
- [x] 5.4 Perform one final `rv`, fix in-scope findings, rerun affected checks, commit the OpenSpec and implementation to the task branch, and freeze the local candidate without push or deploy.
