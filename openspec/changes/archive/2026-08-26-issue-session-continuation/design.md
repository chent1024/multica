## Context

See `proposal.md` for motivation. The server already owns one Issue Session per Issue, associates every Run with a stage occurrence, preserves provider continuation pointers, and exposes task-run summaries plus Issue timeline comments. The current main-column component only reads the session and activates stages; the complete per-Run list and transcript buttons remain in the right sidebar.

The product is a single local Electron workbench with one local Codex agent and SQLite. React Query owns all Issue, session, Run, and timeline server state. Stage activation deliberately has no execution or delivery side effect.

## Goals / Non-Goals

**Goals:**

- Give the current-work surface one truthful conversation action for first start, continuation, and already-active work.
- Keep session continuation idempotent and bound to the current stage occurrence.
- Derive the visible latest outcome from existing Run and timeline truth.
- Explain only actionable Issue-status conflicts.
- Make inherited handoff concise without losing provenance or content.

**Non-Goals:**

- A configurable workflow, progress percentage, stage approval gate, or new evidence database.
- Automatic stage progression, automatic commit/push/release, or deployment controls.
- Replacing the execution log, transcript dialog, Issue timeline, status catalog, or comment composer.
- Generating or rewriting handoff content with an LLM.

## Decisions

### 1. Add one bounded continuation endpoint

Add `POST /issues/{id}/session/continue`. It first returns an existing active Run, preventing double clicks and concurrent callers from creating duplicates. Otherwise it requires an online local Codex runtime and a runnable Issue, aligns only an unassigned backlog Issue to local Codex/in-progress, and queues one Run against the active session stage using the existing continuation-pointer logic.

The response contains the authoritative Issue and Run so the client can update both caches immediately. The endpoint never activates a stage. A finished Issue or an Issue assigned to another actor is rejected instead of being silently reassigned.

Alternative: reuse `/rerun`. Rejected because retrying a selected historical Run and continuing the durable Issue Session have different first-run, active-run, and assignment semantics.

Alternative: chain existing Issue update and rerun calls in the client. Rejected because a partial first write could leave the UI aligned while no Run was queued, and concurrent clicks could race.

### 2. Reuse the transcript and existing query ownership

The workspace reads the same per-Issue task query key as the execution log. An active Run opens through the existing transcript component. Starting or continuing calls the new mutation, installs the returned Issue and Run into React Query, then opens that transcript. No transcript implementation or second Run store is added.

### 3. Project one current-stage outcome

A pure view model selects Runs by exact active `stage_id`, newest first. The visible outcome is the latest associated agent timeline comment when available, then the Run summary, then the bounded error text; otherwise the UI says no outcome is recorded. The model never parses prose into commit, push, release, or completion flags.

### 4. Keep status broader than stage

Stage and Issue status remain separate server fields. The UI labels the stage explicitly and only warns for backlog/done/cancelled categories. The continuation command aligns an unassigned backlog Issue because starting work is the user-authorized state change; a stage-recording click never changes status.

Alternative: maintain a universal stage-to-status mapping. Rejected because stages are optional/repeatable and commit, push, and release do not map cleanly to the Issue status catalog.

### 5. Collapse handoff detail, enrich read provenance

The default handoff shows objective, parent, readable source stage, and at most two representative items. A native details disclosure contains every non-empty section. The handoff read query joins the persisted source stage occurrence to return its kind; no schema migration or editable copy is introduced.

## Risks / Trade-offs

- [A runtime can disappear after preflight] → Keep Run insertion and Issue alignment in one store transaction after selecting the runtime; a later daemon disconnect leaves a truthful queued Run rather than a false success.
- [Timeline outcome can be long] → Render a bounded preview and open the existing transcript/timeline for full content.
- [Repeated stage kinds can mix evidence] → Match the exact active stage occurrence ID, not only the stage kind.
- [The main card can grow again] → Show one latest outcome and a bounded handoff summary; keep full history behind existing disclosures.
- [Old servers lack readable source stage] → Make the new field additive in the client schema while the updated local server always supplies it.

## Migration Plan

1. Add contract/store/API tests for active, first-start, continuation, conflict, and source-stage projection.
2. Add client mutation/cache and pure evidence-model tests.
3. Replace the stage-only primary action and compact handoff in the existing component.
4. Run focused tests, full type/backend checks, and the real Electron path.
5. Rollback is a code revert; no database migration or persisted-state rewrite is involved.
