## 1. Product Model and Information Architecture

- [x] 1.1 Add a pure, typed stage-presentation model for all seven stages: localized goal/evidence keys, recommended successor, alternate choices, history summary, and continuity severity.
- [x] 1.2 Replace the sidebar Session Workflow with one main-column current-work module positioned after the Issue description and before sub-issues/activity.
- [x] 1.3 Present handoff objective, parent provenance, and populated inherited-context sections inside the main module.
- [x] 1.4 Keep stage history and healthy continuity behind progressive disclosure; render an explicit recovery warning for continuity gaps.

## 2. Interaction and Product Copy

- [x] 2.1 Add one primary recommended next-stage action where one exists and a secondary accessible menu for every other non-active stage.
- [x] 2.2 Preserve optimistic activation, authoritative reconciliation, failure rollback, and an accessible inline error state.
- [x] 2.3 Make the record-only boundary explicit so commit, push, and release never imply or trigger execution.
- [x] 2.4 Add complete English, Simplified Chinese, Japanese, and Korean copy for stage goals, evidence, progression, recovery, handoff, and technical details.
- [x] 2.5 Keep the existing Execution Log as secondary run inspection with past runs collapsed; do not duplicate task controls in the current-work module.

## 3. Frontend and Backend Verification

- [x] 3.1 Add view-model unit tests for every stage recommendation, release terminal behavior, alternate-stage choices, repeated history, and continuity severity.
- [x] 3.2 Replace sidebar-oriented component tests with main-product behavior tests covering wide/narrow desktop placement semantics, primary and alternate transitions, rollback, record-only messaging, gap recovery, and handoff rendering.
- [x] 3.3 Add or extend Issue detail composition tests proving one current-work surface appears in the main column and none appears in the sidebar.
- [x] 3.4 Run focused `packages/views` and `packages/core` tests plus TypeScript typecheck.
- [x] 3.5 Run focused Go handler/store tests for Issue Session read, repeated activation, idempotent activation, and invalid stage behavior to validate the reused backend contract.
- [x] 3.6 Run the highest stable local Electron user-path smoke at wide and minimum supported window widths, and report any blocked visual layer explicitly.

## 4. Validation and Delivery

- [x] 4.1 Run `openspec validate issue-session-product-workspace --strict` and keep the change artifacts aligned with the final behavior.
- [x] 4.2 Run the repository's highest applicable final check (`make check`) and distinguish each validated layer from any environment limitation.
- [x] 4.3 Perform one final `rv`, fix only in-scope findings, and rerun affected checks.
- [x] 4.4 Commit the OpenSpec and implementation on the task branch using the repository commit convention, then freeze the local candidate without push or deploy.
