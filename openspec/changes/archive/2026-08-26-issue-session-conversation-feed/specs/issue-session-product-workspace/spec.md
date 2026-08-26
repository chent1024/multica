## MODIFIED Requirements

### Requirement: Current work is part of the Issue narrative

The product SHALL show the active Issue Session stage in the Electron Issue detail main content after the task definition. The same main column SHALL present one chronological conversation feed across all Runs and stage occurrences, followed by one continuation composer. Human requests and agent responses SHALL remain primary content; compact stage and Run-state markers MAY separate turns without turning the surface into an operations panel. The properties sidebar SHALL NOT duplicate these controls.

#### Scenario: User opens an Issue on desktop

- **WHEN** the Issue, timeline, Runs, and Issue Session load
- **THEN** the main content shows current-work guidance and one continuous conversation before the continuation composer
- **AND** prior Run conversations do not require opening separate dialogs to read their user-visible requests and outcomes

#### Scenario: User narrows the desktop window

- **WHEN** the Issue detail renders at the product's supported minimum window width
- **THEN** the current stage, conversation content, queued-turn state, and composer remain usable in one column

### Requirement: Progression is guided but non-linear

The product SHALL make sending a message through the Issue conversation composer the primary way to start or continue work. One accepted send SHALL create one visible user turn and one Run. Stage recording SHALL remain a separate secondary action that recommends at most one next stage and permits any other non-active stage. Opening technical Run details SHALL remain available but SHALL NOT be required to continue the conversation. The product SHALL NOT represent stage occurrences as mandatory steps, completion percentage, or a fixed one-way sequence.

#### Scenario: User opens an active conversation

- **WHEN** the Issue has an active Run and the user opens its detail
- **THEN** the active turn and Run state are already visible in the conversation feed
- **AND** no new Run or stage occurrence is created until the user sends another message

#### Scenario: User continues completed work

- **WHEN** the Issue has no active Run and the user submits the composer
- **THEN** the atomic Issue Session message command is called once
- **AND** the accepted user turn appears in the feed and the composer clears

#### Scenario: User sends while work is active

- **WHEN** a Run is active and the user submits another message
- **THEN** the accepted user turn appears in the feed with an explicit waiting state
- **AND** the product does not imply that two Runs are executing concurrently

#### Scenario: User advances from analysis

- **WHEN** analysis is active and the user chooses the recommended stage action
- **THEN** the existing stage-activation command is called for implementation
- **AND** no message or Run is created by the stage action

#### Scenario: User revisits implementation after review

- **WHEN** review is active and the user selects implementation from the alternate stage control
- **THEN** a new implementation occurrence is requested
- **AND** the prior review occurrence remains visible in the conversation history

#### Scenario: User is at release

- **WHEN** release is active
- **THEN** no automatic successor is presented
- **AND** the composer and alternate stage actions remain available for follow-up work

## ADDED Requirements

### Requirement: Conversation feed preserves product and technical hierarchy

The default conversation feed SHALL show top-level human comments, their replies, agent final responses, stage transitions, Run waiting/active/completed/failed state, and continuity recovery warnings from authoritative Issue Session, timeline, and Run data. Raw tool calls, full transcripts, token usage, provider identifiers, and diagnostics SHALL remain in existing secondary inspectors. Loading and failure of session or Run projections SHALL NOT hide already loaded comments or disable ordinary comment replies.

#### Scenario: Multiple Runs span multiple stages

- **WHEN** an Issue contains user turns and agent responses across analysis and implementation
- **THEN** the feed preserves chronological reading order and labels the relevant stage boundaries
- **AND** the user-visible content is readable without opening the execution log

#### Scenario: User needs technical detail

- **WHEN** a Run marker or agent response has an associated Run
- **THEN** the user can open the existing technical transcript or execution inspector
- **AND** raw tool traffic is not expanded in the default feed

#### Scenario: Run projection fails after comments load

- **WHEN** comments are available but Run or session metadata fails to load
- **THEN** comments and replies remain readable and usable
- **AND** the missing metadata is reported as a bounded retryable state
