# issue-session-workflow Specification

## Purpose
Keeps all work for one issue in one durable logical conversation while making optional delivery stages and provider-thread continuity observable and recoverable.
## Requirements
### Requirement: One durable session per issue
The system SHALL create and retain exactly one logical Issue Session for every issue. Deleting an issue SHALL delete its session-owned stages, handoffs received by that issue, and run associations in the same application transaction.

#### Scenario: New issue receives a session
- **WHEN** a user creates an issue
- **THEN** the response and subsequent session read identify one stable Issue Session owned by that issue

#### Scenario: Existing issue is upgraded
- **WHEN** the application opens a database containing issues created before this capability
- **THEN** each existing issue receives one session without losing its comments, activities, attachments, or task runs

### Requirement: Optional ordered stages
An Issue Session SHALL maintain an ordered history of stage occurrences using the supported kinds `analysis`, `implementation`, `review`, `test`, `commit`, `push`, and `release`. Exactly one occurrence SHALL be active while the session is open; moving forward or returning for revision SHALL append a new occurrence and preserve prior history. A user SHALL NOT be required to activate every supported stage.

#### Scenario: Advance to implementation
- **WHEN** the active stage is analysis and the user activates implementation
- **THEN** analysis is completed and a new active implementation occurrence is appended

#### Scenario: Return for revision
- **WHEN** the active stage is review and the user activates implementation
- **THEN** the completed review occurrence remains in history and a new implementation occurrence becomes active

#### Scenario: Skip unused stages
- **WHEN** a user moves directly from implementation to commit
- **THEN** the session records commit as active without creating review or test occurrences

### Requirement: Runs belong to the session and active stage
Every newly queued task run SHALL belong to the issue's Issue Session and to the stage occurrence active when the run is queued. Historical runs SHALL remain visible under their original stage after the session advances.

#### Scenario: Queue a run in review
- **WHEN** the current stage is review and a run is queued
- **THEN** the run is associated with the Issue Session's active review occurrence

### Requirement: Logical continuity is independent of provider thread identity
The Issue Session SHALL remain stable when the provider-native thread changes. A later run SHALL request the latest usable provider thread and work directory from a prior run in the same Issue Session. The system SHALL report whether the new run started fresh, resumed the prior provider thread, or continued with a continuity gap.

#### Scenario: Provider thread resumes
- **WHEN** a later run reports the same provider thread identifier that it was offered from its prior run
- **THEN** the run is marked resumed and remains in the same Issue Session

#### Scenario: Provider thread is replaced
- **WHEN** a later run cannot use the offered provider thread and reports a different thread identifier
- **THEN** the run remains in the same Issue Session and exposes a continuity gap referencing the prior run

#### Scenario: First run has no predecessor
- **WHEN** the first run in an Issue Session starts
- **THEN** it is marked fresh rather than as a continuity failure

### Requirement: Stage selection has no delivery side effect
Activating commit, push, or release SHALL only record the session's current stage. It SHALL NOT commit, push, deploy, publish, or mutate an external runtime without a separately initiated and authorized run.

#### Scenario: Activate release
- **WHEN** a user changes the active stage to release
- **THEN** the stage is recorded and no deployment command is executed

### Requirement: Issue detail exposes session workflow
The issue detail surface SHALL show the stable Issue Session, current stage, ordered stage history, each run's stage, and continuity state. Loading, empty, and failed session reads SHALL remain distinguishable.

#### Scenario: Review a multi-stage issue
- **WHEN** a user opens an issue whose session contains analysis, implementation, and review runs
- **THEN** the page identifies review as current and retains the earlier stage and run history

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
