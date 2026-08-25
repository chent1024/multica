## Purpose

Give users a coherent Issue-centered journey from analysis through release by presenting the current goal, expected evidence, and next decision while keeping execution telemetry and delivery authorization secondary and truthful.

## ADDED Requirements

### Requirement: Current work is part of the Issue narrative

The product SHALL show the active Issue Session stage in the Electron Issue detail main content, after the task definition and before downstream work and activity. The properties sidebar SHALL NOT be the only location or a duplicate location for stage controls.

#### Scenario: User opens an Issue on desktop

- **WHEN** the Issue and its Issue Session load
- **THEN** the main content shows a current-work surface before sub-issues and activity
- **AND** the properties sidebar does not show a second stage selector

#### Scenario: User narrows the desktop window

- **WHEN** the Issue detail renders at the product's supported minimum window width
- **THEN** the current stage and its next action remain available in the main content

### Requirement: Every active stage explains its product meaning

For analysis, implementation, review, test, commit, push, and release, the product SHALL show a localized stage name, one user goal, and concrete evidence that belongs in the existing Issue description or timeline.

#### Scenario: User views the review stage

- **WHEN** review is the active stage
- **THEN** the surface explains that scope and correctness are being checked
- **AND** it identifies a review result and findings as the expected evidence

#### Scenario: User changes locale

- **WHEN** any supported product locale is active
- **THEN** stage names, goals, evidence, actions, recovery messages, and technical-detail labels render from that locale without falling back to raw keys

### Requirement: Progression is guided but non-linear

The product SHALL offer at most one primary recommended next-stage action for the active stage and SHALL provide a secondary way to activate any other non-active stage. It SHALL NOT represent stage occurrences as mandatory steps, completion percentage, or a fixed one-way sequence.

#### Scenario: User advances from analysis

- **WHEN** analysis is active and the user chooses the recommended action
- **THEN** the existing stage-activation command is called for implementation
- **AND** the UI reflects the mutation's optimistic state and authoritative response

#### Scenario: User revisits implementation after review

- **WHEN** review is active and the user selects implementation from the alternate stage control
- **THEN** a new implementation occurrence is requested
- **AND** the prior review occurrence remains visible in stage history

#### Scenario: User is at release

- **WHEN** release is active
- **THEN** no automatic successor is presented
- **AND** alternate stages remain available for follow-up work

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
