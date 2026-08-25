# child-issue-handoff Specification

## Purpose
Lets a child issue begin in its own session with a bounded, traceable snapshot of the parent issue context instead of copying an unbounded conversation transcript.
## Requirements
### Requirement: Child issue receives a structured handoff
When a child issue is created with handoff content, the system SHALL persist one immutable handoff snapshot containing its objective and optional confirmed facts, decisions, scope constraints, acceptance criteria, relevant references, and open questions.

#### Scenario: Create child with handoff
- **WHEN** a user creates a child issue and supplies structured handoff content
- **THEN** the child Issue Session exposes the handoff with the supplied fields

### Requirement: Handoff provenance is traceable
Every handoff SHALL identify its parent issue, parent Issue Session, source stage occurrence, and optional source run. The handoff SHALL link to those sources rather than duplicating the complete parent transcript.

#### Scenario: Inspect handoff source
- **WHEN** a user opens a child issue with an inherited handoff
- **THEN** the handoff identifies the parent issue and the exact source stage and run when available

### Requirement: Handoff creation is bounded and atomic
The system SHALL enforce field and collection size limits for handoff input. Child issue, child Issue Session, initial stage, and handoff creation SHALL succeed or fail as one application transaction.

#### Scenario: Invalid handoff is rejected
- **WHEN** a child issue request exceeds a handoff limit or references a source run outside the parent Issue Session
- **THEN** the request fails without creating the child issue or a partial handoff

### Requirement: Child execution receives inherited handoff
Every run in the child Issue Session SHALL receive the immutable handoff snapshot as issue context, while later parent changes SHALL NOT silently rewrite the snapshot.

#### Scenario: Parent changes after child creation
- **WHEN** the parent issue is edited after a child handoff was created
- **THEN** the child continues to receive the original snapshot and can follow its source links for newer context

### Requirement: Child issue exposes inherited context
The child issue detail surface SHALL display the inherited handoff separately from ordinary comments and SHALL provide navigation to the parent issue.

#### Scenario: Open child issue
- **WHEN** a user opens a child issue that has a handoff
- **THEN** the page shows the handoff objective, populated sections, and parent link without expanding the parent transcript
