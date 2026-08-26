# project-owned-tasks Specification

## Purpose
Ensure every local task has one explicit Git-project context so task organization and Codex execution always resolve to the intended repository without exposing a cloud workspace concept.
## Requirements
### Requirement: Every task has one project owner

The system SHALL require every newly created task to reference an existing local project. Once a task has a project, the project SHALL be immutable; a legacy task without a project MAY select one project once. The system SHALL reject an empty, unknown, or different project value without partially creating or updating the task.

#### Scenario: Create a task with a project
- **WHEN** a client creates a task with an existing project identifier
- **THEN** the task is created under that project
- **AND** its project context is returned by subsequent reads

#### Scenario: Reject projectless creation
- **WHEN** a client creates a task without a project or with an unknown project
- **THEN** the request is rejected
- **AND** no task, session, comment, attachment binding, or Run is persisted

#### Scenario: Preserve established project ownership
- **WHEN** a client attempts to clear or replace the project of a task that already has one
- **THEN** the request is rejected
- **AND** the original project remains unchanged

#### Scenario: Repair a legacy projectless task
- **WHEN** a legacy task has no project and the user selects an existing project
- **THEN** that project becomes the task's permanent owner

### Requirement: Child tasks inherit the parent project

The system SHALL create every child task in its parent task's project. A supplied child project SHALL either match the parent project or be rejected.

#### Scenario: Create child without repeating project
- **WHEN** a child task is created for a project-owned parent without a project in the request
- **THEN** the child inherits the parent's project

#### Scenario: Reject cross-project child
- **WHEN** a child task request names a project different from its parent
- **THEN** the request is rejected without creating the child or its handoff

### Requirement: Projects with tasks cannot be deleted

The system SHALL reject deletion of a project while any task belongs to it and SHALL NOT orphan or cascade-delete those tasks.

#### Scenario: Delete a project that owns tasks
- **WHEN** the user requests deletion of a project with one or more tasks
- **THEN** deletion is rejected with an actionable conflict
- **AND** the project and tasks remain unchanged

#### Scenario: Delete an empty project
- **WHEN** the user deletes a project that owns no tasks
- **THEN** the project and its resources are deleted

### Requirement: New-task surfaces require project context

Manual and Codex task creation SHALL expose project as a required field. A project-scoped entry SHALL preselect that project, an installation with exactly one project SHALL preselect it, and a global entry with multiple projects SHALL require an explicit selection. When no projects exist, task creation SHALL direct the user to add a local project rather than submit a task.

#### Scenario: Create from a project
- **WHEN** the user opens task creation from a project surface
- **THEN** that project is selected
- **AND** the task cannot be submitted without it

#### Scenario: Create with one available project
- **WHEN** task creation opens outside a project and exactly one project exists
- **THEN** that project is selected automatically

#### Scenario: Create globally with several projects
- **WHEN** task creation opens outside a project and several projects exist
- **THEN** no project is selected implicitly
- **AND** submission remains unavailable until the user selects one

#### Scenario: Create before adding a project
- **WHEN** no local project exists and the user starts task creation
- **THEN** the surface explains that a project is required
- **AND** provides a direct action to add a project

### Requirement: Local navigation does not expose a workspace product

The local desktop SHALL present Tasks, Projects, Agents, Runtime, and Settings without a workspace switcher or workspace grouping label. The fixed internal local data-space identifier MAY remain as a compatibility detail but SHALL NOT be presented as a user-owned container.

#### Scenario: Open the local sidebar
- **WHEN** the local desktop navigation renders
- **THEN** it contains no workspace selector or workspace group label
- **AND** the Tasks view remains a cross-project aggregate

### Requirement: Legacy projectless data is repaired without a placeholder project

On upgrade, the system SHALL assign legacy projectless tasks automatically only when exactly one existing project makes the choice unambiguous. Otherwise the tasks SHALL remain readable and SHALL require an explicit one-time project selection before they can execute or be otherwise continued. The system SHALL NOT create a non-Git placeholder project or guess among multiple projects.

#### Scenario: Upgrade with one project
- **WHEN** legacy projectless tasks exist and exactly one project exists
- **THEN** the upgrade assigns those tasks to that project

#### Scenario: Upgrade with ambiguous project ownership
- **WHEN** legacy projectless tasks exist with zero or multiple projects
- **THEN** their content remains readable
- **AND** execution and continuation are unavailable until the user explicitly assigns a project
