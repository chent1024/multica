## Why

Multica currently records each Codex execution as an independent task run even though users reason about one durable issue conversation that moves from analysis through delivery. This fragments continuity, makes phase ownership implicit, and leaves child tasks without a reliable, bounded transfer of the parent issue's decisions and constraints.

## What Changes

- Give every issue exactly one durable Issue Session that owns its ordered execution history.
- Allow a session to move through optional stages such as analysis, implementation, review, test, commit, push, and release without requiring every stage.
- Bind every task run to the issue's session and current stage while preserving the distinction between the logical session and the provider-native Codex thread.
- Resume the most recent usable provider thread and work directory for later runs in the same issue; expose an explicit continuity gap when resumption is unavailable.
- Create structured handoff snapshots when a child issue is created from a parent issue, containing the child objective, confirmed facts, decisions, scope constraints, acceptance criteria, relevant references, and open questions.
- Surface the current stage, stage history, run continuity, and inherited handoff on the issue detail page.
- Keep push and release as explicit, user-authorized stages; entering or displaying a stage never performs either operation automatically.
- Backfill existing issues and runs into one default Issue Session without deleting current timeline or task history.

## Capabilities

### New Capabilities

- `issue-session-workflow`: One durable session per issue, optional ordered stages, run-to-stage binding, provider-thread continuity, continuity-gap reporting, and the issue-detail workflow surface.
- `child-issue-handoff`: Structured, bounded context transfer from a parent Issue Session into a child Issue Session, with traceable source references.

### Modified Capabilities

None. This repository has no existing OpenSpec capabilities.

## Impact

- SQLite schema initialization and forward upgrades in `server/internal/localworkbench`.
- Local workbench Store and HTTP routes for session, stage, and handoff reads/writes.
- Daemon claim/session reporting so later runs receive the latest usable provider session and work directory.
- Shared core schemas, types, queries, and mutations.
- Desktop issue-detail UI and focused Go/React behavior tests.
- No account, cloud workspace, team, invitation, billing, push automation, or deployment automation is introduced.
