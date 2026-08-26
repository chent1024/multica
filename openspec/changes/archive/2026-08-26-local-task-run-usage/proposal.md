## Why

The local Codex adapter already collects per-model token usage and the daemon reports it, but the loopback server discards the report. Issue execution logs therefore cannot show truthful run or issue totals even though the client types and inspectors already accept usage data.

## What Changes

- Persist each Run's complete per-provider, per-model token-usage snapshot in the local SQLite database.
- Replace a Run's stored snapshot transactionally and idempotently, including an explicit empty snapshot that clears prior rows.
- Return each Run's recorded usage from the existing Issue task-runs interface and return compatible Issue-level token sums from the existing Issue usage interface.
- Retry transient usage-report failures for a short bounded interval without delaying the terminal Run callback for minutes.
- Refocus the Issue usage inspector on processed tokens, input cache hit rate, output tokens, Run count, duration, and per-Run token details instead of cloud cost or agent-spend analysis.
- Preserve unknown historical usage as unknown rather than presenting it as zero.

## Capabilities

### New Capabilities

- `local-task-run-usage`: Durable, idempotent local Run usage accounting and token-focused Issue inspection.

### Modified Capabilities

None.

## Impact

- `server/internal/localworkbench`: one forward-only SQLite table migration, explicit Issue cleanup, usage write/read handlers, and Run hydration.
- `server/internal/daemon`: bounded transient retry for the existing usage-report call; Codex parsing remains unchanged.
- `packages/views/issues/components` and issue locale resources: token-focused Issue and Run usage presentation using the existing task-runs React Query data.
- Existing daemon and Issue HTTP paths remain the only interfaces; no duplicate endpoint, account, billing, workspace, tenant, navigation, dashboard, cross-Issue aggregate, deployment, or production write is introduced.
