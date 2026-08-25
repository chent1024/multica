## Context

See `proposal.md` for motivation. The existing Issue page already owns the product narrative: title and description define the task, child issues split the work, and Comment/Event timeline entries carry progress, commits, verification, and release records. The sidebar owns properties and detailed task runs. `IssueSessionSection` currently puts the logical session into that sidebar as seven equal pills plus raw continuity and handoff data.

The server already guarantees one durable Issue Session per Issue and returns the active stage, ordered stage occurrences, continuity state/reason, and optional child handoff. Stage activation is transactional, idempotent for the current kind, and deliberately has no delivery side effect. A new persisted stage-output model would duplicate the existing timeline and is therefore outside the product model.

## Goals / Non-Goals

**Goals:**

- Make the user's current objective, expected evidence, and next decision visible in both wide and narrow layouts of the desktop Issue flow.
- Preserve the truthful semantics that stages are optional, repeatable, and may be revisited.
- Use one primary recommendation without hiding alternate transitions.
- Keep context loss actionable and healthy execution metadata quiet.
- Centralize stage presentation rules in one tested view model so labels, guidance, and next-stage choices cannot drift across render branches.

**Non-Goals:**

- Building an operations dashboard, workflow engine, approval system, or autonomous stage progression.
- Executing a Run, commit, push, release, deployment, or status update from a stage transition.
- Persisting stage artifacts, completion percentages, checklists, or custom stage catalogs.
- Reworking the Issue timeline, task transcript, usage accounting, issue status model, or child-issue creation contract.
- Adding a separate Session page or a second navigation model for the same Issue.
- Adding a web or mobile product surface to this Electron-only repository.

## Product Journey

| Stage | User goal | Evidence already owned by the Issue | Recommended progression |
| --- | --- | --- | --- |
| Analysis | Agree on the problem, scope, and acceptance boundary | Description, decisions, questions, and analysis updates in the timeline | Implementation |
| Implementation | Produce the smallest complete change | Code-changing Run and implementation update in the timeline | Review |
| Review | Check scope, correctness, and missed requirements | Review result and any findings in the timeline | Test; return to Implementation when findings require changes |
| Test | Prove behavior at affected frontend/backend layers | Test commands, results, and observable acceptance in the timeline | Commit; return to Implementation on failure |
| Commit | Freeze the reviewed candidate | Commit SHA and subject in the timeline | Push |
| Push | Publish the exact candidate to the authorized remote branch | Remote ref and read-back SHA in the timeline | Release |
| Release | Put an explicitly authorized SHA into the target environment | Version, smoke result, and rollback reference in the timeline | No automatic next step; revisit any stage when follow-up work is needed |

## Decisions

### 1. Put the work journey in the main column

Render the session product module after the description and before sub-issues/activity. It is part of completing the Issue, remains visible when the desktop content area narrows, and can refer naturally to evidence recorded below.

Alternative considered: keep improving the sidebar card. Rejected because the sidebar is optional and semantically aligned with metadata and inspection rather than the primary task journey; the main journey must also survive the desktop window's minimum supported width.

### 2. Use a recommendation plus an escape hatch, not a linear stepper

The active stage gets one recommended next action. A secondary stage menu exposes every other stage, including earlier stages. Release has no presumed successor. Re-selecting the active stage is not presented as work.

Alternative considered: a seven-step progress bar. Rejected because the backend intentionally permits skipping and repeating stages; a percentage or mandatory order would be fabricated product state.

### 3. Derive presentation from existing persisted truth

A pure view-model module maps the active stage and session state to goal copy, evidence copy, recommendation, compact history, and recovery severity. React Query remains the owner of server state, and the existing optimistic mutation/rollback path remains the only stage writer.

Alternative considered: persist stage goals, evidence, or completion flags. Rejected because the stage catalog is fixed, Issue Comment/Event is already the evidence record, and new storage would create a second source of truth.

### 4. Separate progress recording from work execution

The main action is worded as recording movement to the next stage. Nearby copy states that it does not run Git or deployment operations. There is no commit/push/release execution callback in this component.

Alternative considered: launch a Run from the stage CTA. Rejected because stage selection and execution authorization are separate contracts, and Git delivery/deployment require explicit scope not represented by this control.

### 5. Apply progressive disclosure to technical state

A continuity gap renders an inline recovery warning telling the user to review the last recorded outcome before continuing. Healthy continuity and full stage occurrence history live in a details region, while the no-side-effect explanation stays next to the stage actions. Detailed runs and usage remain in the sidebar's existing Execution Log and past runs stay collapsed.

Alternative considered: remove technical details. Rejected because continuity and history are useful for diagnosis, but they must not dominate the default product hierarchy.

### 6. Treat handoff as inherited working context

When present, the handoff appears in the main product module with objective first, parent navigation, and only non-empty structured sections. It remains immutable and traceable to the parent.

Alternative considered: copy handoff fields into the description. Rejected because that loses provenance and creates editable duplication.

## Risks / Trade-offs

- [Static guidance can become generic] → Keep each stage to one concrete goal and a short evidence list grounded in existing Issue artifacts; do not add a configurable framework.
- [A recommended next stage can look mandatory] → Label it as recommended, retain a complete alternate-stage menu, and avoid progress percentages or completion checkmarks for skipped stages.
- [Commit/push/release wording can imply authorization] → Say that the action records progress only and test that no run or delivery callback exists.
- [The new main module can crowd the Issue] → Use a compact flat card, collapse history/technical details, and render handoff sections only when populated.
- [Moving the component can regress narrow desktop layouts] → Keep it inside the existing responsive max-width column and test at the product's supported minimum window width without relying on sidebar content.

## Migration Plan

1. Introduce the view model and redesigned component behind the same existing Issue Session query/mutation contract.
2. Place it in the main Issue content and remove its old sidebar instance in the same change so there is one stage control.
3. Run frontend behavior/type tests and focused backend API/store regression tests for the reused contract.
4. Run the highest stable Electron/local UI smoke available; report any environment-blocked visual layer as unknown rather than inferred.
5. Rollback is a code revert: no database or API migration is involved.
