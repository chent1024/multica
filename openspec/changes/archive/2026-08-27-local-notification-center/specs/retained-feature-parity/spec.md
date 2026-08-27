## MODIFIED Requirements

### Requirement: Retained upstream surfaces remain present
Desktop SHALL retain Search, command palette, Notifications through the compatible internal Inbox contract, Chat, My Issues, Issues, Projects, Automations, Agents, Squads, Statistics, Runtimes, Skills, Settings, onboarding, diagnostics, updates, and every supporting workflow classified `keep` or `keep/localize` in `feature-parity.md`. The localized Notifications surface SHALL present non-collaboration events defined by the local notification center rather than removed cloud collaboration events.

#### Scenario: Navigation is audited
- **WHEN** localized Desktop navigation and routes are compared with baseline `09a2410e`
- **THEN** every retained surface remains reachable under the upstream information architecture, with Notifications using the retained internal Inbox route
