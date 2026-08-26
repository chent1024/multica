# issue-session-product-workspace Specification

## Purpose
Give users a coherent Issue-centered journey from analysis through release by presenting the current goal, expected evidence, and next decision while keeping execution telemetry and delivery authorization secondary and truthful.
## Requirements
### Requirement: Current work is part of the Issue narrative

The product SHALL show the active Issue Session stage in the Electron Issue detail main content after the task definition. The same main column SHALL present one chronological conversation feed across all Runs and stage occurrences, followed by one continuation composer. Human requests and agent responses SHALL remain primary content; compact stage and Run-state markers MAY separate turns without turning the surface into an operations panel. The properties sidebar SHALL NOT duplicate these controls.

#### Scenario: User opens an Issue on desktop

- **WHEN** the Issue, timeline, Runs, and Issue Session load
- **THEN** the main content shows current-work guidance and one continuous conversation before the continuation composer
- **AND** prior Run conversations do not require opening separate dialogs to read their user-visible requests and outcomes

#### Scenario: User narrows the desktop window

- **WHEN** the Issue detail renders at the product's supported minimum window width
- **THEN** the current stage, conversation content, queued-turn state, and composer remain usable in one column

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

### Requirement: Stage changes do not execute work or authorize delivery

The product SHALL state that stage activation records workflow progress only. Activating commit, push, or release SHALL NOT start a Run, invoke Git, change an Issue status, deploy software, or perform another delivery side effect.

#### Scenario: User records the push stage

- **WHEN** the user activates push
- **THEN** only the existing stage-activation request is made
- **AND** the UI explains that the push itself requires a separately authorized execution

#### Scenario: Stage activation fails

- **WHEN** the stage-activation request fails
- **THEN** optimistic state is rolled back
- **AND** the current-work surface shows an accessible inline error without claiming the stage changed

### Requirement: Continuity problems are actionable and healthy telemetry is quiet

The product SHALL visibly warn when the Issue Session reports a continuity gap and tell the user how to recover using existing Issue evidence. Healthy continuity state, detailed stage history, and run metadata SHALL remain secondary through progressive disclosure or the existing run inspector.

#### Scenario: Session reports a context gap

- **WHEN** continuity state is `gap`
- **THEN** the main surface warns that execution context could not be fully resumed
- **AND** it tells the user to review the latest recorded outcome before continuing
- **AND** the warning is not conveyed by color alone

#### Scenario: Session continuity is healthy

- **WHEN** continuity state is `fresh`, `pending`, or `resumed`
- **THEN** raw continuity status does not compete with the current goal and next action in the default hierarchy

### Requirement: Child handoff supports the current work

When a child Issue has a handoff, the product SHALL show its objective and parent provenance in the main work surface and SHALL show only non-empty structured handoff sections. The handoff SHALL remain read-only.

#### Scenario: Child has inherited context

- **WHEN** the Issue Session includes a handoff
- **THEN** the child objective is visible near the current-stage guidance
- **AND** the user can navigate to the exact parent Issue
- **AND** populated facts, decisions, constraints, acceptance criteria, references, and open questions are available without empty headings

### Requirement: Existing backend session contract remains authoritative

The product SHALL obtain session, stage history, continuity, and handoff from the existing Issue Session read endpoint and SHALL activate stages through the existing bounded stage command. No client-derived stage persistence or duplicate stage-output store SHALL be introduced.

#### Scenario: Issue Session is unavailable

- **WHEN** the session read is loading or fails
- **THEN** the main work location shows a bounded loading or retryable error state
- **AND** the rest of the Issue detail remains usable

#### Scenario: Backend accepts a repeated stage

- **WHEN** the existing API appends a repeated stage occurrence
- **THEN** the product renders the server-returned occurrence history without deduplicating it into a false linear checklist

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
