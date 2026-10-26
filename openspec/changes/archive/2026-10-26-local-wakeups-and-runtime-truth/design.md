## Context
The existing local store is schema 22 and uses one SQLite connection with transactional migrations, durable task_runs, activities, local_jobs, and local automation scheduling. Current installed data contains one project and four agents, and no issues/runs/wakeup definitions. The upstream Go service validates Wakeup input independently of PostgreSQL; reuse its pure validation/schedule calculations and event vocabulary.

## Decisions
- Append immutable additive SQLite migrations; do not modify migrations 1–22. Keep SQLite index syntax as required by sqlite-persistence, rather than PostgreSQL CONCURRENTLY syntax.
- Persist rule configuration separately from execution/revision/next-fire state. Reuse existing task_runs for execution; occurrence and event receipt keys atomically prevent duplicate dispatch across ticks/restarts. No second daemon or scheduler service.
- Capture local durable mutations through committed SQLite event records and evaluate conditions against current local facts. Provider dispatch uses the existing agent/squad runtime target resolution.
- Preserve terminal issue and disabled/deleted rule semantics, stale revision conflicts, prospective subscriptions, actor/run filters, timeouts, continuous limits, and child-stage completion. Surface unavailable prerequisites as actionable state rather than synthetic success.
- Add runtimes.metadata TEXT JSON with an empty default. Registration populates actual cli_version/launched_by and provider version separately; legacy rows do not infer CLI version from provider version. Running daemon IPC does not promote server-offline providers.

- Append duplicate pointer and workflow/project binding storage using the existing Issue and Project owners. Reuse upstream pure issueworkflow normalization/brief logic. Map statuses and cancel affected old runs transactionally; use existing task_runs and local identity for actual handoffs, with no parallel workflow engine.

## Data Safety and Recovery
Before replacing the app, stop only the idle desktop-owned runtime, snapshot the current profile/database and installed app, test schema 22 upgrades against that snapshot, and compare original projects/agents/resources identifiers and content after migration. Installation uses the existing electron-vite/electron-builder configuration. On failure stop the new runtime and restore the backed-up app and complete profile together; do not run destructive down-migrations against user data.

## Validation
Use real local router/store tests with fake harnesses for schedule/event/condition dispatch and terminal completion; test duplicate concurrent ticks, restart, failed transaction, invalid scope/actor/revision, and migration preservation. Re-run impacted desktop tests/typecheck and local race tests. Inspect the installed native app and its actual API/daemon versions, runtime offline states, Wakeup configuration/summary/run history, and console/network failures. A real paid agent run is not part of default tests; use the existing daemon protocol with a test fake in isolated acceptance data when execution is needed.
