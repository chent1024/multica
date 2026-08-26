# issue-surface-view-preferences Specification

## Purpose
Keep each Issue surface's local display preferences durable across restarts while allowing focused defaults to evolve once without erasing later user choices.
## Requirements
### Requirement: Existing Issue surfaces adopt focused status-column defaults once

When the focused board default changes, the product SHALL migrate each older persisted Issue surface once by adding the newly hidden status categories to that surface's existing hidden categories. The migration SHALL apply independently to every stored surface and SHALL preserve categories the user had already hidden.

#### Scenario: Older surface showed every status category

- **WHEN** an older persisted Issue surface has no hidden status categories
- **THEN** its first load after the upgrade hides backlog, blocked, and cancelled

#### Scenario: Older surface already hid another category

- **WHEN** an older persisted Issue surface already hides one or more categories
- **THEN** the migrated surface keeps those categories hidden and also hides backlog, blocked, and cancelled

#### Scenario: Registry contains several surfaces

- **WHEN** one persisted registry contains workspace, project, or saved-view surfaces from the older version
- **THEN** every stored surface is migrated independently

### Requirement: Later user visibility choices remain durable

After the one-time migration, the product SHALL preserve subsequent user choices to show or hide status categories across reloads and SHALL NOT reapply the focused defaults to an already migrated registry.

#### Scenario: User shows a focused-default category after migration

- **WHEN** the user shows backlog, blocked, or cancelled after the registry has migrated
- **THEN** that category remains shown after the next reload
