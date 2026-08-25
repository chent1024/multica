## Why

The Issue detail now explains the active session stage, but its primary action only records a stage change while the actual conversation remains hidden behind per-run controls in the sidebar. This makes the product say “continue” without continuing work, permits visibly contradictory Issue/session state, and shows generic evidence guidance instead of the outcomes already stored on runs and the Issue timeline.

## What Changes

- Add one explicit Issue Session continuation command that returns an existing active Run or queues the next Run in the same durable session. For an unassigned parked Issue, this user-authorized action assigns local Codex and moves the Issue to in-progress before queuing; it never changes the session stage or performs Git/delivery actions.
- Make the current-work primary action start, resume, or open the actual Issue conversation. Rename stage actions so they truthfully record stage changes rather than imply execution.
- Project current-stage Run status and outcome from the existing task-run and Issue timeline records; do not add a stage-output store or parse arbitrary delivery state into new persisted fields.
- Explain actionable Issue-status/session-stage conflicts without silently coupling all stage transitions to Issue status.
- Reduce child handoff’s default footprint while retaining every inherited item behind progressive disclosure, and show the source parent stage returned by the existing handoff read model.
- Preserve the right-side execution log as secondary technical history; do not create an operations dashboard, workflow engine, automatic commit/push/release, or configurable stage framework.

## Capabilities

### New Capabilities

- None.

### Modified Capabilities

- `issue-session-workflow`: Add an explicit, idempotent session-continuation command and source-stage provenance for child handoff.
- `issue-session-product-workspace`: Make current work resumable, evidence-backed, status-aware, and compact for child Issues while preserving non-linear stage recording.
- `child-issue-handoff`: Present readable source-stage provenance and keep the complete immutable snapshot available through progressive disclosure.

## Impact

- Backend: local Issue Session API/store, task queuing transaction boundary, handoff read projection, and focused API/store tests.
- Core: Issue Session continuation types/schema/client/mutation and React Query cache updates.
- Views: Issue Session workspace model/component, Issue detail integration, localization, and behavior tests.
- No database migration, new dependency, external service, Git side effect, push, or deployment.
