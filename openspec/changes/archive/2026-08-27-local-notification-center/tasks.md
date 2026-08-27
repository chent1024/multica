## 1. Durable notification contract

- [x] 1.1 Add a forward SQLite migration for notification source identity, actor, and structured details with an idempotency index and upgrade coverage
- [x] 1.2 Replace the ad hoc Inbox insert path with a validated transactional local-notification helper that returns whether a row was newly created
- [x] 1.3 Extend Inbox scanning, API schemas, and compatibility parsing for the complete local notification payload and legacy rows

## 2. Local outcome producers

- [x] 2.1 Emit deduplicated review-request, failed or blocked, and quick-create outcome notifications from committed Agent task transitions
- [x] 2.2 Emit deduplicated action-required notifications from committed failed automation runs
- [x] 2.3 Emit deduplicated action-required notifications from persisted integration, Webhook, and plugin execution failures
- [x] 2.4 Emit actionable Runtime or credential notifications only for failed requested operations, excluding transient liveness changes
- [x] 2.5 Publish `inbox:new` only for newly committed notifications and verify replay, rollback, reconnect, and unread behavior

## 3. Local Desktop experience

- [x] 3.1 Rename the local Desktop navigation and notification page chrome to Notifications while preserving shared Inbox copy and internal identifiers
- [x] 3.2 Map supported notification details to exact issue or finite local recovery destinations without accepting arbitrary URLs
- [x] 3.3 Add local list mode and system-banner preferences, hide collaboration-only controls in Desktop, and keep shared preference parsing compatible

## 4. Verification and closeout

- [x] 4.1 Add focused Go tests for migration, durable fields, idempotency, each retained producer family, preference filtering, and post-commit realtime publication
- [x] 4.2 Add focused TypeScript tests for compatibility parsing, Desktop naming, destination mapping, preference controls, realtime cache updates, and system-banner muting
- [x] 4.3 Run affected Go and TypeScript tests, typecheck, strict OpenSpec validation, and a restart-backed development App smoke test
- [x] 4.4 Run the final `rv`, repair only in-scope findings, mark the OpenSpec tasks complete, and freeze the uncommitted local candidate
