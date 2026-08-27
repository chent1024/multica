## Context

See `proposal.md` for motivation. The local product already retains `/inbox`, SQLite `inbox_items`, React Query hooks, read/archive operations, realtime `inbox:new`, and native notification handling. Its only current local producer is the Agent terminal callback; that producer writes incomplete rows with source-specific type and severity values outside the shared contract, then commits without publishing `inbox:new`. Other retained local execution systems persist their outcomes independently.

The implementation must follow the repository's SQLite-only migration rules, preserve shared Web and Mobile contracts, and keep route and storage identifiers stable. Server state remains owned by React Query, while Desktop-only presentation differences stay behind an existing platform boundary.

## Goals / Non-Goals

**Goals:**

- Make every local notification a durable projection of an already committed or atomically committing source outcome.
- Give all producers one small insertion contract with stable identity, supported severity, structured details, and safe local navigation metadata.
- Reuse the retained Inbox API, realtime event, React Query cache, and system-notification pipeline.
- Keep shared views usable while presenting local-only names and preferences in Desktop.

**Non-Goals:**

- Recreate member, invitation, mention, comment, subscription, reaction, tenant, or remote synchronization workflows.
- Rename `/inbox`, database tables, API clients, query keys, or the `inbox:new` event.
- Add arbitrary URL actions, a generalized event bus, notification templates, retries, digests, or remote push delivery.
- Notify for transient Runtime heartbeat changes or errors already synchronously shown to the operator without a committed background outcome.

## Decisions

### 1. Extend the existing SQLite row instead of adding a second notification model

A forward SQLite migration will add nullable actor fields, structured `details`, and a nullable `source_key` to `inbox_items`, plus a partial unique index scoped by local data space, recipient, and non-null source key. Existing rows remain readable with empty details and no source identity. New local producers must provide a source key.

This preserves read/archive behavior and avoids parallel local and upstream models. A separate event table was rejected because it would duplicate lifecycle state and require a second projection pipeline.

### 2. Use one transactional insertion helper and publish only after commit

The local server will expose one internal notification input and insertion helper. The helper validates the finite local type/severity contract, serializes details, performs an insert that reports whether a new row won the unique source key, and returns the complete item needed by realtime consumers. A producer that owns a database transaction inserts alongside its outcome; producers whose outcome is already committed use a short follow-up transaction keyed to that immutable execution result.

Callers collect only newly inserted rows and publish `inbox:new` after a successful commit. Replayed callbacks therefore neither duplicate rows nor emit duplicate events. Publishing before commit and periodically scanning for unseen failures were rejected because they can expose rolled-back state or add an unnecessary polling subsystem.

### 3. Derive source identity from persisted execution IDs

Source keys use a producer namespace plus the persisted execution identifier and terminal meaning, for example an Agent task review request or failure, an automation run failure, a Webhook delivery failure, or a plugin invocation failure. Quick-create origin is identified from its persisted source context so its terminal task can use quick-create-specific types without inventing a second execution. Runtime or credential notifications are attached to the failed requested execution or durable configuration test, never to liveness polling.

This makes retries distinguishable while making callback replay idempotent. Timestamp-only keys were rejected because replay would duplicate notifications.

### 4. Keep navigation structured and local

Issue-related rows retain `issue_id`. Non-issue rows store a finite local destination kind and object identifier in `details`; the client maps those values to known internal routes. Failure details preserve the concrete local error when available. Arbitrary action URLs are not accepted or executed.

This keeps notification data useful after restart without turning persisted plugin or integration output into a navigation injection surface.

### 5. Add a local preference projection without deleting shared fields

The preference payload will add a local list mode (`all` or `action_required`) and retain the existing system-notification toggle. Local Desktop settings render only those controls. Existing collaboration fields remain parseable for shared Web and Mobile source compatibility but are not used to decide which local events are created.

Filtering is applied by the list API so unread counts and pagination match the selected local mode. Muting system banners remains client delivery behavior and never suppresses durable insertion. Reusing individual collaboration toggles was rejected because their labels and semantics do not describe a single-operator product.

### 6. Rename only at the Desktop presentation boundary

Desktop navigation and page chrome use a Notifications locale key when running in the local shell. Shared Web and Mobile keep their existing Inbox copy and internal identifiers. This avoids an unrelated cross-platform product rename while making the local UI truthful.

## Risks / Trade-offs

- [A producer persists its failure before notification insertion] → Use the immutable execution ID as the source key and insert immediately after the outcome; a replay remains safe and API reconciliation exposes any successfully inserted row.
- [Repeated Runtime liveness changes create noise] → Notify only a user-requested execution or durable credential/configuration check that reaches an actionable failure state.
- [Old rows have no source key or details] → Keep new columns nullable/defaulted and parse old rows defensively; deduplication is guaranteed for new producers only.
- [Server-side list filtering changes unread counts] → Apply the same saved mode to list and unread queries and test both projections.
- [Shared view edits accidentally rename Web] → Gate new copy and simplified controls with the existing Desktop platform check and retain current shared locale keys.

## Migration Plan

1. Apply the forward SQLite migration transactionally; old rows remain valid and the partial unique index only governs new non-null source keys.
2. Deploy the extended API scanner and insertion helper before switching producers to the new contract.
3. Add producers one persisted execution family at a time and verify duplicate replay plus post-commit realtime behavior.
4. Enable local Desktop naming, navigation mapping, and simplified preferences after the server contract is available.
5. On rollback, the prior binary ignores added SQLite columns and continues reading existing rows. Newly written supported type values remain parseable by the existing lenient client schema; no destructive down migration is required.
