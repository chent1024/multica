## Why

Local tasks can currently be created without a project even though Codex execution depends on a project-owned working directory. This leaves tasks without a safe Git context and keeps the obsolete cloud-workspace vocabulary visible in the single-machine product.

## What Changes

- **BREAKING** Require every newly created task to reference an existing local project.
- Inherit and validate project ownership for child tasks, and reject attempts to clear a task's project.
- Prevent deleting a project while it still owns tasks instead of orphaning them.
- Make project selection required in manual and Codex task creation, automatically selecting the current or only project and guiding an empty installation to add a Git project first.
- Remove the user-visible workspace grouping from local navigation while retaining the fixed internal local data-space compatibility boundary.
- Upgrade legacy projectless tasks without inventing a non-Git placeholder project.

## Capabilities

### New Capabilities

- `project-owned-tasks`: Defines mandatory project ownership, creation defaults, legacy repair, child-task inheritance, project deletion protection, and local navigation behavior.

### Modified Capabilities

- `issue-session-workflow`: Requires every Issue Session and its Runs to retain the Issue's project working context.

## Impact

- SQLite schema and forward upgrades in `server/internal/localworkbench`.
- Local issue, quick-create, child-task, update, project-delete, and daemon validation paths.
- Core request types and React Query mutations.
- Shared task creation, project picker, task list, and sidebar views.
- Existing projectless test fixtures and affected Go/Vitest coverage.
