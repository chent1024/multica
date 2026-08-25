## 1. Backend continuation contract

- [x] 1.1 Add failing store/API tests for active-run idempotency, first start, prior-session continuation, terminal/foreign-assignee/runtime errors, and unchanged stage identity
- [x] 1.2 Implement the bounded Issue Session continuation store transaction and HTTP endpoint with authoritative Issue + Run response
- [x] 1.3 Add readable source-stage kind to handoff reads and cover it through the API contract

## 2. Core client contract

- [x] 2.1 Add failing schema/client tests for continuation response and additive handoff provenance
- [x] 2.2 Implement continuation types, schema, API client, and React Query mutation/cache updates

## 3. Product workspace

- [x] 3.1 Add failing pure-model tests for exact-stage Run selection, real outcome priority, active/empty states, and bounded handoff summary
- [x] 3.2 Add failing component tests for start/continue/open conversation, truthful stage-recording copy, status conflicts, actual outcomes, error handling, and complete handoff disclosure
- [x] 3.3 Implement the current-work conversation action, outcome projection, status guidance, compact handoff, source-stage label, and Issue detail integration using existing components and tokens
- [x] 3.4 Update all supported locales without raw-key fallbacks

## 4. Verification and closeout

- [x] 4.1 Run focused frontend/core/backend tests and TypeScript checks
- [x] 4.2 Run the real Electron flow for start/open/continue, stage recording, evidence, handoff disclosure, requests, console, keyboard, and wide/minimum supported layouts
- [x] 4.3 Run one rv, fix in-scope findings, rerun affected verification, validate and archive OpenSpec, then commit and freeze the local candidate
