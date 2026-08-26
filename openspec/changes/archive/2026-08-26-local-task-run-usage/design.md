## Context

See `proposal.md` for motivation. The daemon already converts agent usage into one row per provider and model, and the existing Issue execution-log client model already accepts `usage` on each Run. The missing seam is the local workbench: its usage report handler is a no-op, its Run query omits usage, and SQLite has no durable usage table.

SQLite initialization executes the current schema and then transactional forward upgrades. The repository prohibits foreign keys and cascading deletes. The Issue usage dialog currently derives its totals directly from `task-runs[].usage`; the separate Issue usage query is a compatibility interface and is not a second UI state owner.

## Goals / Non-Goals

**Goals:**

- Put validation, ownership checking, snapshot replacement, and reading behind a small local store interface.
- Preserve a Run's provider/model dimensions so mixed-model Runs remain truthful.
- Make retry replay safe and keep terminal reporting responsive.
- Reuse the existing task-runs React Query entry and Issue inspector.

**Non-Goals:**

- Incremental live usage while a Run is active or a durable daemon outbox.
- Cross-Issue, project, daily-trend, runtime, dashboard, or navigation usage surfaces.
- Changing Codex token parsing, restoring cloud billing, or removing shared pricing code used outside the local Issue surface.
- Treating Run completion as task-goal acceptance.

## Decisions

### 1. Store one normalized row per Run, provider, and model

Create `task_run_usage` with `(task_id, provider, model)` as its primary key, non-negative SQLite checks, nullable `cost_usd_ticks`, and `updated_at`. The Run remains the owner of status and timing; the usage table contains only normalized counters.

Alternative considered: add counters to `task_runs`. Rejected because one Run may use multiple models and pricing or diagnostics must not guess which model owns a combined counter.

### 2. Treat every report as a complete replaceable snapshot

Decode and validate the full body first. Inside one database transaction, prove that the Run joins an Issue in the fixed local data space, delete its prior rows, insert the new rows, and commit. Empty rows deliberately perform the delete only. Duplicate provider/model pairs are rejected rather than silently coalesced because the daemon already owns accumulation and accepting duplicates would make caller bugs invisible.

Alternative considered: upsert only reported rows. Rejected because a later corrected snapshot could no longer remove an obsolete model row.

### 3. Hydrate Run usage in bounded bulk reads

List Runs first, then select all usage rows whose task IDs belong to the requested Issue and group them by task ID. The single-Run projection uses the same row shape. This keeps the task-runs interface authoritative without an N+1 query or a JOIN that duplicates Run rows.

The Issue summary interface independently performs SQL sums plus a Run count from the same tables. It returns raw sums only; derived cache rate remains presentation logic.

### 4. Use a dedicated short retry schedule

The usage client reuses the existing transient-error classifier and retry helper with a short schedule. It continues to report synchronously before the terminal callback so the existing terminal event invalidation observes persisted usage. The empty-snapshot client shortcut is removed to make the complete-snapshot contract internally consistent.

Alternative considered: reuse the 124-second terminal retry schedule. Rejected because usage failure could double the delay before users see a terminal Run. A durable outbox is also rejected as disproportionate to this local first version.

### 5. Keep raw usage in React Query and derived metrics in the view

The existing task-runs query remains the only state owner for the Issue inspector. Shared token summarization is extended with a cache-hit helper that sums counters before dividing and returns unknown for a zero denominator. The Issue UI removes cost-specific subscriptions and sections but leaves shared runtime pricing modules untouched.

Alternative considered: fetch the Issue summary interface for the dialog. Rejected because the Run table still needs the same task-runs response and two independent queries could temporarily disagree.

## Risks / Trade-offs

- [The daemon exits or the server stays unavailable beyond the short retry window] → Log the failure and continue terminal delivery; durable offline delivery remains explicitly out of scope.
- [A replay changes `updated_at`] → Time windows are not part of this change and Run timestamps remain authoritative; repeated snapshots do not affect Run ordering or duration.
- [Historical Runs lack usage] → Return empty arrays and render unknown, never zero.
- [Cost fields remain in transport and storage] → Keep compatibility without presenting cost in the local Issue product surface.

## Migration Plan

1. Add the table to fresh schema initialization and a transactional version-9 forward upgrade using `CREATE TABLE IF NOT EXISTS`.
2. Deploy store and handler behavior together; no backfill is possible because historical token usage was discarded.
3. Hydrate existing Run responses and update the Issue inspector in the same candidate.
4. Validate reopening a pre-version-9 database, snapshot replacement, terminal paths, client parsing, and Issue UI behavior.
5. Rollback is a code revert that leaves the additive table unused; production recovery remains fix-forward and no destructive down migration is added.
