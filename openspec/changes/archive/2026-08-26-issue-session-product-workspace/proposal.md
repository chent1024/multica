## Why

The current Issue Session UI exposes seven equal stage controls, raw history, and continuity status in the Issue sidebar. Users have to interpret workflow machinery instead of seeing what the current stage is trying to accomplish, what evidence belongs on the Issue, and what they should do next; in narrow desktop layouts, the core workflow is displaced into an optional properties surface.

## What Changes

- Put a product-oriented current-work surface in the Issue detail's main narrative, after the issue description and before child work and activity, so it remains available in the desktop product's narrow content layouts.
- Give every stage a concise user goal, expected evidence, and one recommended next-stage action while preserving optional, repeatable, and out-of-order stage transitions.
- Make alternate stage selection secondary and clearly state that stage changes record progress only; they do not execute commit, push, release, or any other run.
- Present child handoff as inherited working context with parent provenance, not as an operational data block.
- Elevate a continuity gap as a user-facing recovery warning, while keeping healthy continuity, identifiers, detailed history, runs, and usage behind progressive disclosure or in the existing run area.
- Remove the duplicate Session Workflow controls from the properties sidebar; keep Issue properties and run inspection available without making them the primary completion path.
- Reuse the existing Issue Session API and data model. Add no stage-output table, workflow automation, or delivery authorization.

## Capabilities

### New Capabilities

- `issue-session-product-workspace`: A main-column Issue work journey that explains the active stage, evidence, recommended progression, inherited context, recovery state, and the boundary between recording progress and executing work.

### Modified Capabilities

None.

## Impact

- `packages/views/issues/components`: Issue detail composition and a redesigned Issue Session product module.
- `packages/views/locales`: Complete English, Simplified Chinese, Japanese, and Korean product copy for all stages and states.
- `packages/core/issues/session`: Existing query/mutation behavior remains the client boundary and receives regression coverage where needed.
- `server/internal/localworkbench`: Existing session read and stage-activation handlers remain unchanged but their contract is exercised by focused backend tests.
- No database migration, public API expansion, permission change, automatic Git action, push, deployment, or release is introduced.
- No web or mobile product surface is introduced; the scope remains the local Electron workbench.
