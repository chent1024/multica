## Context

Upstream `main` at `09a2410e882be8435bd6c4a26e03f7e288038203` already contains the product behavior to preserve. Its Desktop client reaches a Go HTTP/WebSocket server whose infrastructure assumes cloud identity and workspaces, PostgreSQL/sqlc, Redis coordination, S3-compatible blobs, and publicly reachable callbacks. The previous local branch proved Electron loopback and SQLite are viable, but also removed product surfaces; it is evidence only and is not merged into this candidate.

This change localizes infrastructure, not product domains. Existing routes, views, client interfaces, domain semantics, automations, Squads, Agents, Skills, integrations, and Statistics remain the acceptance baseline unless a capability is exclusively a cloud account/control-plane concern.

## Goals / Non-Goals

**Goals:**

- Preserve the upstream Desktop information architecture, product interfaces, and complete retained workflows.
- Replace cloud dependencies at six narrow seams: identity context, persistence, runtime coordination, file storage, process lifecycle, and callback reachability.
- Keep one fixed workspace-shaped compatibility context so shared callers do not require a broad rewrite.
- Make SQLite initialization and forward upgrades transactional, deterministic, and recoverable by fix-forward migrations.
- Prove retained functionality through the feature-parity matrix, including failure and restart behavior.

**Non-Goals:**

- Importing hosted PostgreSQL customer data.
- Supporting cloud multi-user collaboration, invitations, subscriptions, billing, quotas, managed runtimes, or cross-device synchronization.
- Replacing or redesigning retained product pages to make backend localization easier.
- Making Web or Mobile a supported local runtime; their source remains unless shared-contract compilation requires a scoped edit.
- Maintaining cloud and local infrastructure adapters in the shipped Desktop runtime.

## Decisions

### 1. Localize through infrastructure seams

Desktop keeps the upstream client and product interfaces. Local adapters replace only infrastructure responsibilities, concentrating change behind deep modules rather than distributing `localMode` branches across handlers and views.

Alternative: reimplement one local backend per product domain. Rejected because it duplicates product behavior, enlarges the interface callers must understand, and caused prior feature loss.

### 2. Preserve one fixed compatibility identity context

Bootstrap returns one deterministic local operator and one deterministic workspace-shaped data-space identity. Existing workspace path parameters, headers, query keys, and records remain compatibility inputs, while signup, login, tenant switching, membership, invitations, and ACL decisions disappear from the local runtime.

Alternative: remove workspace identity from all shared interfaces. Rejected because it changes thousands of callers without product value.

### 3. Put all durable product state behind one SQLite persistence module

The persistence module owns the connection, transaction policy, schema ledger, IDs, timestamps, error mapping, and repositories required by retained handlers. SQLite is the only product database. Relationship checks and dependent cleanup occur in application transactions; schema foreign keys and cascades are not introduced. Index creation uses `CREATE INDEX IF NOT EXISTS`.

Fresh initialization and ordered forward upgrades use the same entrypoint. A failed upgrade rolls back atomically. Once shipped, migration bodies are immutable; recovery is a new forward migration.

Alternative: translate PostgreSQL migrations and sqlc queries mechanically. Rejected because PostgreSQL-specific DDL and cloud tables do not define the local persistence contract.

### 4. Replace Redis by responsibility, not by emulation

A local runtime module owns queues, claims, scheduler occurrences, locks, liveness, rate limits, cache invalidation, and event fan-out. Restart-sensitive facts are persisted transactionally in SQLite; ephemeral cache and in-process fan-out remain memory-owned. Interfaces express the business operation rather than Redis commands.

Alternative: embed or emulate Redis. Rejected because it retains an unnecessary external operational model and leaks Redis semantics into local callers.

### 5. Store blobs atomically under the Desktop data directory

A file-storage adapter writes temporary files, fsyncs as appropriate, atomically renames completed content, and stores only normalized relative identities in SQLite. Reads and deletes validate containment under the configured root. Attachments, avatars, artifacts, and integration payload files retain their upstream metadata and HTTP behavior.

### 6. Electron owns the loopback runtime lifecycle

Electron chooses isolated database/file paths and a loopback port, starts the bundled Go server, waits for a health/identity handshake, reconnects WebSocket clients, and terminates the child on shutdown. The server rejects non-loopback product traffic. Development and packaged startup share the same contract.

### 7. Preserve integrations with explicit reachability

Locally implemented outbound integrations use local credentials and normal network access. Inbound Webhook and VCS callbacks bind to loopback and expose their callback URL/reachability state. Integrations requiring Internet-originated callbacks can use a user-provided relay/tunnel URL. Provider-hosted OAuth, Composio, and channel execution paths remain present in source and settings but are disabled in the local runtime until their external provider prerequisites and local execution adapter exist; the API reports that boundary instead of hiding the surface or claiming it works.

### 8. Delete only proven cloud-control-plane entrypoints

Removal happens after retained workflows no longer call the old dependency. Eligible removals are signup/login/verification, cloud workspace lifecycle, human membership/invitations/tenant ACL, subscription/billing/quota/sales, remote synchronization, managed cloud runtimes, and hosted telemetry upload. Ambiguous features default to retained.

## Verification Strategy

Each retained feature must trace through its real Desktop entrypoint, client parsing, local handler, SQLite transaction or local runtime responsibility, WebSocket/refetch projection, actionable failure state, and restart recovery. Navigation visibility, compilation, HTTP 200, or a unit test alone is insufficient.

The final candidate must pass fresh-database and upgrade tests, offline startup without PostgreSQL/Redis/S3, concurrency tests for claims and scheduling, file containment tests, integration reachability tests, affected TypeScript and Go tests, Desktop build/package checks, and the feature-parity matrix.

## Risks / Trade-offs

- Hidden membership assumptions in retained handlers may surface late. Remove authorization at the identity seam, not with scattered UI fallbacks.
- SQLite serializes writes differently from PostgreSQL. Keep transactions short and test claim, scheduler, and Run update contention.
- Inbound Internet callbacks cannot reach loopback by themselves. Make reachability an explicit configuration and observable state.
- The prior local branch may encode reduced behavior. Reuse only narrowly inspected fragments that satisfy this specification.
- Full parity is larger than a database swap. Keep the product interface stable and migrate responsibility-by-responsibility so failures remain attributable.

## Delivery

Implementation stays on `codex/remove-cloud-sqlite-v2`. The prior localized branch and upstream baseline remain read-only. After one final `rv`, exact-candidate verification, and OpenSpec archive, freeze the local candidate; commit, push, promotion, and deployment require separate authorization.
