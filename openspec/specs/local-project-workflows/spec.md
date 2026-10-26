# local-project-workflows Specification

## Purpose
TBD - created by archiving change local-wakeups-and-runtime-truth. Update Purpose after archive.
## Requirements
### Requirement: Workflow configuration is durable and follows the upstream contract
The SQLite runtime SHALL support Default and named workflow CRUD, ordered existing/new status columns, owner/admin local identity, column handlers and instructions, project assignment, copy-for-project, dry-run mapping plans, required remappings and deletion protections. Changes SHALL persist and publish committed workflow/project/issue updates.
#### Scenario: Edit a workflow and rebind a project
- **WHEN** a local user edits columns or switches a project workflow
- **THEN** the server provides the required mapping plan and applies valid remapping atomically while retaining all issues and history

### Requirement: Entering a workflow step performs the real handoff
Issue creation and applicable status/project transitions SHALL validate the target workflow, resolve none/agent/squad/member/project-lead/creator handlers, preserve explicit confirmation requirements and prevent stale or duplicate handoffs. Agent/squad handoffs SHALL queue real local tasks with the configured brief through the existing execution owner; unavailable runtimes SHALL remain actionable. Terminal/removed steps SHALL not dispatch, and previous runs SHALL be handled according to the existing upstream transition contract.
#### Scenario: Status enters an agent column
- **WHEN** an issue enters an agent-handled workflow status
- **THEN** its assignee and queued run match that handler and contain the column instructions, visible in native UI and claimable by the local daemon protocol
#### Scenario: Invalid or stale transition
- **WHEN** a request selects an excluded status, invalid handler, or stale handoff confirmation
- **THEN** the transition rejects without partially changing the issue or executing old instructions
