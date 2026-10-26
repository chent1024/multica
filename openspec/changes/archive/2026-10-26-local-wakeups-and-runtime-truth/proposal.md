## Why
The merged upstream desktop polls Wakeup endpoints that the SQLite fork does not implement. It also displays offline harnesses as online and reports provider binary versions as the Multica CLI version. The user explicitly requests full local Wakeup behavior, preservation of existing data, reinstallation, and final acceptance without committing or pushing.

## What Changes
- Implement the existing upstream Wakeup HTTP contract on SQLite: user rules, summaries, workspace filters, run history, management/revisions, and child-done system rules/defaults.
- Execute time/event/condition rules with durable occurrence receipts, transactional dispatch into existing local task runs, expiry, pause limits, and restart recovery.
- Preserve provider availability through desktop IPC and distinguish provider version from authoritative daemon CLI metadata.
- Upgrade the installed SQLite database additively only after snapshot-copy validation and an offline backup; retain a recoverable copy of the installed app and profile.

## Capabilities
### New Capabilities
- `local-issue-duplicates`: Durable one-level duplicate marking, reopening, deletion cleanup and relation views.
- `local-project-workflows`: Existing workflow editor, project assignment, status mapping, handoff previews and real local handler dispatch.
- `local-issue-wakeups`: Full local implementation of existing Wakeup rules, scheduling, lifecycle and projections.
### Modified Capabilities
- `local-runtime-services`: Accurate provider liveness and separate daemon/provider metadata.

## Impact
Local SQLite store/migrations, local HTTP routes, local scheduling and task integration, desktop daemon IPC cache updates, regression tests, packaging and native acceptance. Existing project/agent/issue identifiers, directories, and data are retained. No PostgreSQL migration or cloud service is used. The user subsequently authorized full local duplicate relationships and project workflows after populated-state acceptance exposed their missing endpoints. No commit or push.
