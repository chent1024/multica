## Context

See `proposal.md` for motivation. The current Issue page obtains a vanilla view store per workspace, project, or saved-view surface from one persisted registry. Individual surface states are merged over current defaults, so a stored empty `hiddenStatusCategories` array correctly wins during ordinary hydration. The registry itself is still persistence version 0 and does not run the versioned migration defined for the older standalone view stores.

## Goals / Non-Goals

**Goals:**

- Upgrade every stored surface entry at the registry persistence boundary.
- Make the upgrade idempotent through the persistence version, not through ordinary state merging.
- Preserve valid stored surface metadata and all unrelated view preferences.

**Non-Goals:**

- Clearing local renderer storage or resetting complete view preferences.
- Changing the default card properties, sub-issue visibility, saved-view definitions, API state, or SQLite data.
- Reapplying focused defaults after a user intentionally changes column visibility.

## Decisions

### 1. Migrate the registry envelope

Set the surface registry persistence version to 1 and migrate the `surfaces` record when loading version 0. For each valid entry, copy its existing state and replace `hiddenStatusCategories` with the unique union of its valid existing array and the focused default categories.

Alternative: invoke the standalone view-store migration from `mergeViewStatePersisted`. Rejected because ordinary hydration cannot distinguish an old empty array from a deliberate post-migration choice to show every category.

Alternative: clear the registry. Rejected because it would discard unrelated per-surface filters, sorting, card display, table layout, and user choices.

### 2. Preserve malformed-entry fallback behavior

The migration only transforms entries with a state object. Other entries remain available to the existing merge/sanitization path instead of expanding this fix into a new registry validation framework. A missing or malformed `hiddenStatusCategories` field is treated as no previously hidden categories for this migration.

### 3. Test the persistence boundary

Seed version 0 JSON into the workspace-aware storage adapter, rehydrate the registry through the existing workspace switch path, and assert both the live surface stores and rewritten version 1 snapshot. Also verify that a version 1 choice to show a default category survives another rehydration.

## Risks / Trade-offs

- [A category duplicated in old storage] -> Construct a unique union during migration.
- [Several surfaces share one registry] -> Map every registry entry rather than migrating only the first opened surface.
- [Migration accidentally becomes a recurring default merge] -> Gate it exclusively on persistence version and test a post-migration user choice.

## Migration Plan

1. Release the registry version and migration with the renderer bundle.
2. Zustand runs the v0-to-v1 transformation on the next hydration and persists the upgraded envelope.
3. Rollback is a code revert; the version 1 snapshot remains valid view state and does not require destructive storage cleanup.
