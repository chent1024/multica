## MODIFIED Requirements

### Requirement: Handoff provenance is traceable

Every handoff SHALL identify its parent issue, parent Issue Session, source stage occurrence, readable source stage kind, and optional source Run. The handoff SHALL link to those sources rather than duplicating the complete parent transcript.

#### Scenario: Inspect handoff source

- **WHEN** a user opens a child issue with an inherited handoff
- **THEN** the handoff identifies the parent issue and readable source stage
- **AND** the exact source occurrence and Run remain available in the handoff contract when present

### Requirement: Child issue exposes inherited context

The child issue detail surface SHALL display the inherited handoff separately from ordinary comments and SHALL provide navigation to the parent issue. Its default view SHALL prioritize the handoff objective and a bounded summary while keeping every populated immutable section available through progressive disclosure.

#### Scenario: Open child issue

- **WHEN** a user opens a child issue that has a handoff
- **THEN** the page shows the handoff objective, parent link, source stage, and a concise summary
- **AND** the user can reveal every populated handoff section without expanding the parent transcript
