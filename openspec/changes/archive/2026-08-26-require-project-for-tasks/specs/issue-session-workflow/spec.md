## MODIFIED Requirements

### Requirement: Runs belong to the session and active stage
Every newly queued task run SHALL belong to the issue's Issue Session, to the stage occurrence active when the run is queued, and to the issue's immutable project working context. Historical runs SHALL remain visible under their original stage after the session advances. The system SHALL reject a new or continued Run when the Issue has no valid project context.

#### Scenario: Queue a run in review
- **WHEN** the current stage is review and a run is queued for a project-owned Issue
- **THEN** the run is associated with the Issue Session's active review occurrence
- **AND** it resolves execution resources from the Issue's project

#### Scenario: Project context is missing
- **WHEN** a legacy projectless Issue attempts to start or continue work
- **THEN** the Run request is rejected before a Run or comment is persisted
- **AND** the user is told to assign a project
