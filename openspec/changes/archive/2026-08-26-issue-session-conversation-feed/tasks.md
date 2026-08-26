## 1. Persistence and backend contract

- [x] 1.1 Extend fresh and upgraded SQLite schemas with nullable Run request/comment linkage and idempotency coverage.
- [x] 1.2 Add backend tests for atomic send, rollback, idempotent retry, waiting follow-up, promotion, cancellation, and source-linked agent output.
- [x] 1.3 Implement the Issue Session message transaction and bounded HTTP endpoint.
- [x] 1.4 Implement terminal-state waiting promotion and update all applicable active/terminal Run projections.

## 2. Core client integration

- [x] 2.1 Add typed response schemas and API client support for the Issue Session message command and waiting Run status.
- [x] 2.2 Add the React Query mutation with authoritative Issue, timeline, Run, session, and list cache reconciliation.
- [x] 2.3 Add core tests for response parsing, idempotency headers, and cache behavior.

## 3. Product conversation feed

- [x] 3.1 Add a deterministic feed projection that merges comments, stage boundaries, and Run states without loading technical task messages.
- [x] 3.2 Replace the Activity framing with the Issue Session conversation hierarchy while preserving comment threads, virtualization, deep links, and secondary technical inspectors.
- [x] 3.3 Route the top-level rich composer through the atomic session message mutation; keep reply behavior comment-only and show queued-turn feedback.
- [x] 3.4 Complete localized copy and component/model tests for desktop, narrow width, loading, failure, idle, active, waiting, completed, failed, and continuity-gap states.

## 4. Verification and closeout

- [x] 4.1 Run focused Go, core, and views tests plus typecheck and OpenSpec strict validation.
- [x] 4.2 Verify the real Electron Issue journey in Chrome DevTools at normal and narrow widths, including send, queued follow-up, reload, and transcript disclosure.
- [x] 4.3 Run the repository full check, perform one bounded rv review, fix in-scope findings, and archive the OpenSpec change in the same candidate.
- [x] 4.4 Commit the reviewed candidate and report exact SHA and delivery state without deploying.
