## ADDED Requirements

### Requirement: User can explicitly continue the Issue Session

The system SHALL expose one Issue-scoped continuation command that returns the existing active Run when one exists or queues exactly one new Run in the Issue Session's active stage. The command SHALL be safe against repeated activation, SHALL preserve the latest usable provider-thread and work-directory continuation pointers, and SHALL NOT change the active session stage.

For an unassigned Issue in the backlog category, the explicit continuation command SHALL assign the local Codex agent and move the Issue to the built-in in-progress status before queuing. It SHALL reject finished Issues, Issues assigned to another actor, and unavailable local runtimes without claiming that work started.

#### Scenario: Open an existing active conversation

- **WHEN** a user continues an Issue that already has a queued or active Run
- **THEN** the command returns that Run
- **AND** it does not queue a duplicate Run or mutate the Issue Session stage

#### Scenario: Continue a prior conversation

- **WHEN** a runnable Issue has no active Run and has a prior usable Run
- **THEN** exactly one new Run is queued in the active stage
- **AND** the Run carries the prior continuation pointer from the same Issue Session

#### Scenario: Start an unassigned parked Issue

- **WHEN** a user explicitly starts the session for an unassigned backlog Issue while the local Codex runtime is online
- **THEN** the Issue is assigned to local Codex and moved to in-progress
- **AND** exactly one Run is queued in the Issue Session's active stage

#### Scenario: Session cannot be continued

- **WHEN** the Issue is finished, is assigned to another actor, or has no online local Codex runtime
- **THEN** the command reports a bounded error
- **AND** it does not queue a Run or claim that the conversation opened
