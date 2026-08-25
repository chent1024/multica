## Context

See `proposal.md` for motivation. The local workbench currently persists issues, comments, activities, task runs, task messages, attachments, projects, and runtimes in SQLite. A task run owns its provider `thread_id`; the daemon already understands `prior_session_id` and `prior_work_dir`, but the local claim response does not populate either and the local session-pin handler discards `work_dir`.

The product remains loopback-only, single-user, and SQLite-backed. Relationships are enforced in application transactions rather than foreign keys. Existing issue and task history must survive an in-place forward upgrade.

## Goals / Non-Goals

**Goals:**

- Introduce one deep `IssueSession` Module whose Store methods own session creation, stage transitions, run binding, continuity classification, and handoff validation.
- Keep UI and daemon callers on a small HTTP interface instead of independently deriving session state from issue status and task history.
- Preserve provider independence by treating Codex thread/work-directory identifiers as resumable execution pointers, not product identity.
- Deliver a complete local vertical slice with observable behavior and compatibility backfill.

**Non-Goals:**

- Automatic phase orchestration, automatic approval, commit, push, or deployment.
- Multiple logical sessions for one issue, arbitrary custom stage catalogs, teams, cloud workspaces, or cross-device synchronization.
- Copying full parent transcripts into child issues or continuously synchronizing parent context into a child handoff.
- Refactoring the shared cloud-oriented view catalog beyond the local paths needed by this capability.

## Decisions

### 1. Persist a session aggregate instead of deriving it from runs

Add `issue_sessions`, `issue_session_stages`, and `issue_handoffs`. `issue_sessions.issue_id` is unique and stores the active stage pointer plus the latest continuity state. Stage rows are immutable occurrences except for closing the active occurrence. This keeps revisiting implementation after review visible as a new occurrence.

Alternative considered: treat the issue row as the session and infer stages from status or activities. Rejected because issue outcome status and execution phase have different lifecycles, and inference cannot represent skipped or repeated stages reliably.

### 2. Extend task runs additively

Add nullable `session_id`, `stage_id`, `work_dir`, and `prior_task_id` columns plus a non-null continuity state with a safe default. Existing runs are backfilled to their issue's session and initial stage in a transactional forward upgrade.

Alternative considered: move runs into a new table. Rejected because it would duplicate the established task interface and increase migration risk without changing observable behavior.

### 3. Make stage transition one transactional command

Expose a session read and one stage-activation command. The Store validates the fixed stage kind, closes the current occurrence, appends the requested occurrence, and updates the session pointer in one transaction. Re-activating the already-active kind is an idempotent no-op.

Alternative considered: CRUD endpoints for stage rows. Rejected because they expose ordering and uniqueness invariants to callers and create a shallow Module.

### 4. Classify continuity from reported provider identity

When a run is queued, record its predecessor and offer that run's usable `thread_id` and `work_dir` to the daemon. The run starts as `fresh` when no predecessor exists and `pending` when resume is requested. When the daemon pins its actual session/work directory, compare the reported thread with the predecessor: equal becomes `resumed`; different or absent prior pointers becomes `gap` only when a predecessor existed. Persist a bounded reason code, not free-form daemon errors.

Alternative considered: assume that sending `prior_session_id` means resume succeeded. Rejected because daemon-side rollout and path gates can intentionally start a fresh provider thread.

### 5. Snapshot child handoff at creation

Extend issue creation with an optional structured handoff. The Store validates source ownership and limits, then creates the child issue, its session/initial stage, attachments, and handoff in one transaction. Arrays are stored as canonical JSON because they are read as one bounded document and are not queried independently.

Alternative considered: synthesize handoff from the entire parent timeline. Rejected because it is unbounded, noisy, privacy-unfriendly, and cannot distinguish confirmed facts from discussion.

### 6. Keep the UI inside the existing issue detail seam

Add a compact Session Workflow section to the existing right sidebar. React Query owns session and handoff server state. A stage picker uses the existing mutation and optimistic-update conventions; handoff content is read-only after child creation. Existing transcript and execution-log surfaces remain the detailed Run view.

Alternative considered: add a separate session route. Rejected because it makes users choose between two pages for the same issue conversation and breaks the one-Issue-one-session mental model.

## Risks / Trade-offs

- [Historical runs do not reveal their real original stage] → Backfill them into one initial implementation occurrence and label that occurrence as migrated; do not fabricate a detailed stage history.
- [A provider session can be unusable despite a stored identifier] → The daemon keeps its existing rollout/work-directory gates; a replacement thread is recorded as a continuity gap without splitting the Issue Session.
- [Stage changes could be mistaken for authorization] → UI copy and HTTP behavior explicitly state that stage activation has no commit, push, or release side effect.
- [Structured handoff adds creation complexity] → Keep objective required, all other sections optional and bounded; expose the editor only when a parent issue is selected.
- [Shared cloud-oriented types contain broader concepts] → Add only local-compatible fields and endpoints; do not revive cloud routes or empty features.

## Migration Plan

1. Create the three new tables and additive task-run columns using the existing transactional schema-upgrade path.
2. Create one session and one migrated implementation stage per existing issue; associate existing runs deterministically.
3. Update issue creation/deletion and task queue/session-pin transactions to maintain the new invariant.
4. Deploy compatible readers and writers together in the desktop bundle. Older databases upgrade on open; new databases receive the complete schema directly.
5. Rollback is application-level: the old binary ignores additive tables/columns and continues reading existing issues/runs. Recovery is forward-only; no destructive down migration is added.
