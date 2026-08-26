## Why

Existing issue surfaces keep a persisted `hiddenStatusCategories: []` snapshot, so they continue to show every board status column after the focused default changed to hide backlog, blocked, and cancelled. The migration was attached to a legacy view-store persistence path rather than the surface registry used by the current Issue page.

## What Changes

- Version the issue-surface view registry and migrate every v0 surface entry once.
- Preserve categories already hidden by the user while adding the focused default categories during that one migration.
- Leave later user show/hide choices untouched after the registry reaches the new version.
- Cover old workspace, project, and multi-surface registry snapshots with focused store tests.

## Capabilities

### New Capabilities

- `issue-surface-view-preferences`: Persist and evolve per-surface Issue board display preferences without losing user choices.

### Modified Capabilities

- None.

## Impact

- Core only: `packages/core/issues/stores/surface-view-store.ts` and its focused tests.
- Local Electron renderer storage receives a one-time client-side preference migration; no API, SQLite, dependency, or navigation change.
- No local preference store is cleared, and no runtime, Git delivery, or deployment action is introduced.
