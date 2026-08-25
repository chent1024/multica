## MODIFIED Requirements

### Requirement: Every active stage explains its product meaning

For analysis, implementation, review, test, commit, push, and release, the product SHALL show a localized stage name, one user goal, and concrete evidence expected in the existing Issue description or timeline. When Runs exist for the active stage occurrence, the product SHALL also show the latest real Run state and its recorded outcome from existing Run and Issue timeline data. It SHALL distinguish an absent outcome from a successful outcome and SHALL NOT create client-derived completion state.

#### Scenario: User views the review stage

- **WHEN** review is the active stage
- **THEN** the surface explains that scope and correctness are being checked
- **AND** it identifies a review result and findings as the expected evidence

#### Scenario: Active stage has a completed Run

- **WHEN** the active stage occurrence has a completed Run with a recorded outcome
- **THEN** the surface shows that real outcome and provides access to the Run conversation
- **AND** it does not replace the outcome with generic expected-evidence copy

#### Scenario: Active stage has no Run

- **WHEN** no Run belongs to the active stage occurrence
- **THEN** the surface says that no result has been recorded for this stage
- **AND** it does not imply that the stage is complete

#### Scenario: User changes locale

- **WHEN** any supported product locale is active
- **THEN** stage names, goals, evidence, actions, recovery messages, and technical-detail labels render from that locale without falling back to raw keys

### Requirement: Progression is guided but non-linear

The product SHALL make starting, continuing, or opening the current Issue conversation the primary action. Stage recording SHALL remain a separate secondary action that recommends at most one next stage and permits any other non-active stage. It SHALL NOT represent stage occurrences as mandatory steps, completion percentage, or a fixed one-way sequence.

#### Scenario: User opens an active conversation

- **WHEN** the active stage has a queued or running Run and the user selects the primary action
- **THEN** the existing Run conversation opens
- **AND** no new Run or stage occurrence is created

#### Scenario: User continues completed work

- **WHEN** the Issue has no active Run and the user selects the primary action
- **THEN** the explicit Issue Session continuation command is called
- **AND** the returned conversation opens without changing the active stage

#### Scenario: User advances from analysis

- **WHEN** analysis is active and the user chooses the recommended stage action
- **THEN** the existing stage-activation command is called for implementation
- **AND** the action is worded as recording a stage rather than executing implementation
- **AND** the UI reflects the mutation's optimistic state and authoritative response

#### Scenario: User revisits implementation after review

- **WHEN** review is active and the user selects implementation from the alternate stage control
- **THEN** a new implementation occurrence is requested
- **AND** the prior review occurrence remains visible in stage history

#### Scenario: User is at release

- **WHEN** release is active
- **THEN** no automatic successor is presented
- **AND** the conversation action and alternate stages remain available for follow-up work

## ADDED Requirements

### Requirement: Issue status and session stage remain truthful together

The product SHALL label Issue status and Issue Session stage as distinct concepts and SHALL surface a concise actionable message when a parked or finished Issue conflicts with active session work. Starting an unassigned parked Issue through the explicit conversation action MAY align its owner and status as specified by the continuation command; merely recording a stage SHALL NOT change Issue status.

#### Scenario: Parked Issue has an active work stage

- **WHEN** an Issue remains in the backlog category while its session stage describes active work
- **THEN** the current-work surface explains that starting the conversation will move the Issue into progress
- **AND** changing the stage alone does not silently change the Issue status

#### Scenario: Finished Issue cannot continue silently

- **WHEN** an Issue is done or cancelled
- **THEN** the conversation action is unavailable
- **AND** the surface tells the user to reopen the Issue before continuing

### Requirement: Current work remains a product surface rather than an operations panel

The current-work surface SHALL prioritize the current goal, the real latest outcome, the conversation action, and the next decision. Detailed Run lists, token usage, raw identifiers, and provider diagnostics SHALL remain in existing secondary inspectors.

#### Scenario: Multiple historical Runs exist

- **WHEN** the user opens an Issue with several historical Runs
- **THEN** current work shows only the latest outcome for the active stage occurrence
- **AND** the existing execution log retains the complete technical history
