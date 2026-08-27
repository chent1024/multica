## Purpose

Makes complete upstream Desktop behavior the acceptance contract while limiting removals to proven cloud account and control-plane capabilities.

## ADDED Requirements

### Requirement: Retained upstream surfaces remain present
Desktop SHALL retain Search, command palette, Inbox, Chat, My Issues, Issues, Projects, Automations, Agents, Squads, Statistics, Runtimes, Skills, Settings, onboarding, diagnostics, updates, and every supporting workflow classified `keep` or `keep/localize` in `feature-parity.md`.

#### Scenario: Navigation is audited
- **WHEN** localized Desktop navigation and routes are compared with baseline `09a2410e`
- **THEN** every retained surface remains reachable under the upstream information architecture

### Requirement: Retained features remain completely usable
Each retained capability SHALL support its applicable create or configure, persistence, read, search, filter, edit, delete or archive, execution or integration side effect, committed state projection, actionable failure, reconnect, and restart behavior through the real Desktop contract.

#### Scenario: Feature only renders
- **WHEN** a retained page is visible but any applicable acceptance step still calls removed cloud infrastructure or is missing
- **THEN** the localization is incomplete and the feature is not hidden, silently degraded, or reported as delivered

### Requirement: Ambiguous capabilities default to retained
A product capability SHALL be removed only when direct evidence shows its user value is exclusively cloud signup or authentication, cloud workspace lifecycle, human membership or invitations, tenant ACL, subscriptions, billing, quotas, remote synchronization, managed cloud runtimes, or hosted telemetry upload.

#### Scenario: Backend uses a cloud dependency
- **WHEN** a useful product workflow currently calls PostgreSQL, Redis, S3, or a hosted endpoint
- **THEN** the dependency is replaced locally and the product workflow remains retained

### Requirement: Commercial semantics do not own local product behavior
Local Statistics, Agents, automations, integrations, and execution SHALL remain usable without subscription, billing, quota, tenant attribution, or cloud price synchronization.

#### Scenario: Desktop runs without commercial state
- **WHEN** no subscription, billing account, quota record, or pricing feed exists
- **THEN** retained configuration, execution, and truthful non-commercial statistics remain available
