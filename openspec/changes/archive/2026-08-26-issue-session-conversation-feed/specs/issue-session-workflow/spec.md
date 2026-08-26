## MODIFIED Requirements

### Requirement: User can explicitly continue the Issue Session

The system SHALL expose one Issue-scoped message command that atomically records one top-level user comment and creates exactly one Run in the Issue Session's active stage. A client retry with the same idempotency key SHALL return the original comment and Run without duplication. The command SHALL preserve the latest usable provider-thread and work-directory continuation pointers and SHALL NOT change the active session stage.

For an unassigned Issue in the backlog category, the command SHALL assign the local Codex agent and move the Issue to the built-in in-progress status before creating the Run. It SHALL reject finished Issues, Issues assigned to another actor, unavailable local runtimes, empty messages, and invalid attachments without persisting a partial comment, Run, or Issue transition.

#### Scenario: Open an existing active conversation

- **WHEN** a user sends a valid message while the Issue already has a queued or active Run
- **THEN** the message command records one waiting follow-up Run instead of returning or duplicating the active Run
- **AND** it does not mutate the Issue Session stage

#### Scenario: Continue a prior conversation

- **WHEN** a user sends a valid message to a runnable Issue with no active Run
- **THEN** the message is recorded as one top-level comment and exactly one Run is queued in the active stage
- **AND** the Run carries the latest usable continuation pointer from the same Issue Session

#### Scenario: Retry the same message

- **WHEN** a client repeats a successful send with the same idempotency key
- **THEN** the command returns the original comment and Run
- **AND** no duplicate comment or Run is created

#### Scenario: Start an unassigned parked Issue

- **WHEN** a user sends a valid message to an unassigned backlog Issue while the local Codex runtime is online
- **THEN** the Issue is assigned to local Codex and moved to in-progress
- **AND** the comment and Run are committed with that Issue transition

#### Scenario: Session cannot be continued

- **WHEN** the Issue is finished, is assigned to another actor, has no online local Codex runtime, or the message payload is invalid
- **THEN** the command reports a bounded error
- **AND** it does not persist any part of the attempted continuation

## ADDED Requirements

### Requirement: Follow-up messages wait behind the current Run

When a user sends a message while another Run is queued or executing for the same Issue Session, the system SHALL persist one waiting Run linked to that message and SHALL expose its waiting state. The daemon SHALL NOT claim that Run until the preceding Run becomes terminal. After the preceding Run becomes terminal, the oldest waiting Run SHALL become claimable and SHALL use the newest usable continuation pointer then available. Cancellation of a waiting Run SHALL prevent it from later becoming claimable.

#### Scenario: Send during active execution

- **WHEN** a user sends a valid follow-up while a Run is active
- **THEN** the message is immediately visible and its Run is recorded as waiting for the prior turn
- **AND** the daemon does not claim both Runs concurrently

#### Scenario: Current Run completes

- **WHEN** the active Run becomes completed or failed and a waiting follow-up exists
- **THEN** the oldest waiting follow-up becomes queued for claim
- **AND** its continuation state is recalculated from persisted Run evidence

#### Scenario: Waiting follow-up is cancelled

- **WHEN** a waiting follow-up Run is cancelled before promotion
- **THEN** it remains terminal and is never offered to the daemon

### Requirement: Agent outcomes identify their source Run

A completed Run's persisted agent response SHALL identify that Run as its source. Existing comments and Runs created before this linkage SHALL remain readable.

#### Scenario: Completed Run records output

- **WHEN** a Run completes with non-empty agent output
- **THEN** the resulting agent comment identifies the exact source Run
- **AND** the Issue conversation can associate the response with the initiating turn
