## 1. Lock the Upstream Contract

- [x] 1.1 Record upstream baseline `09a2410e` and keep/remove decisions for every Desktop navigation, workflow, background job, setting, and integration.
- [x] 1.2 Define the complete-use acceptance chain and default every ambiguous capability to retained.
- [x] 1.3 Add automated navigation/route inventory checks so localization cannot silently hide a retained surface.
- [x] 1.4 Add contract fixtures for the upstream HTTP, WebSocket, and client response shapes used by retained Desktop workflows.

## 2. Fixed Local Identity Context

- [x] 2.1 Introduce deterministic local operator and workspace-compatible identifiers behind the existing request/bootstrap interface.
- [x] 2.2 Bypass login, token refresh, tenant selection, membership, invitation, and ACL decisions in Desktop without spreading local-mode branches through views.
- [x] 2.3 Preserve workspace-shaped paths, headers, query keys, and records as internal compatibility inputs.
- [x] 2.4 Verify fresh launch, deep links, reconnect, and restart enter the same local data space without a cloud account.

## 3. Transactional SQLite Persistence

- [x] 3.1 Add one SQLite persistence module owning connection policy, transactions, IDs, timestamps, typed errors, and repository interfaces.
- [x] 3.2 Add transactional fresh initialization and immutable ordered forward upgrades with a schema ledger and `CREATE INDEX IF NOT EXISTS` indexes.
- [x] 3.3 Port every retained durable record and query used by upstream Desktop workflows; keep relationship validation and dependent cleanup inside application transactions without foreign keys or cascades.
- [x] 3.4 Replace PostgreSQL/sqlc handler dependencies at the persistence seam without changing retained HTTP or client contracts.
- [x] 3.5 Test fresh, upgraded, interrupted-upgrade, contention, constraint, cleanup, and restart behavior.

## 4. Local Runtime Responsibilities

- [x] 4.1 Replace Redis queues and claims with SQLite-durable work plus atomic claim/lease operations.
- [x] 4.2 Replace scheduling and occurrence deduplication with durable local scheduler records and idempotent occurrence keys.
- [x] 4.3 Replace distributed locks, daemon/runtime liveness, request stores, caches, and pub/sub with local modules; remove hosted tenant-rate gates from the single-operator runtime.
- [x] 4.4 Connect local event fan-out to the retained WebSocket and React Query invalidation contracts.
- [x] 4.5 Test concurrent claims, lease expiry, restart recovery, duplicate events, and unavailable runtimes.

## 5. Local File Storage

- [x] 5.1 Add one contained local storage root with atomic writes, normalized relative identities, streaming reads, and safe deletion.
- [x] 5.2 Move retained binary attachment and plugin-package payloads behind the storage interface; keep URL/value-only avatar and integration metadata in SQLite.
- [x] 5.3 Preserve upstream metadata, preview, download, and deletion behavior without S3-compatible storage.
- [x] 5.4 Test traversal rejection, partial writes, restart recovery, missing files, cleanup, and large streaming payloads.

## 6. Electron Loopback Runtime

- [x] 6.1 Add the loopback-only server entrypoint and reject non-loopback product requests without breaking uploads or callbacks.
- [x] 6.2 Make Electron own isolated port, database path, file path, child startup, health/identity handshake, shutdown, restart, and crash recovery.
- [x] 6.3 Bundle the local server and required assets for supported Desktop packages; remove PostgreSQL, Redis, S3, and hosted endpoint prerequisites from this path.
- [x] 6.4 Verify development configuration, packaged startup, isolated checkout ports, offline local-server launch, restart recovery, and clean shutdown.

## 7. Integration Reachability

- [x] 7.1 Preserve locally executable VCS, plugin, and Webhook behavior using local credentials; keep external-provider settings/source and expose unported provider execution as unavailable.
- [x] 7.2 Expose exact loopback callback URLs and support an explicitly configured public relay/tunnel base URL for Internet-originated callbacks.
- [x] 7.3 Report missing credentials, offline network, invalid signatures, duplicate deliveries, and unreachable callbacks as actionable item-level failures.
- [x] 7.4 Verify each locally executable integration's configure, action, deduplication, failure, and restart behavior; verify explicit unavailable state for provider-hosted execution paths.

## 8. Retained Feature Parity

- [x] 8.1 Keep Search, command palette, Inbox, Chat, My Issues, Issues, Projects, Automations, Agents, Squads, Statistics, Runtimes, Skills, Settings, onboarding, diagnostics, and updates visible under the upstream Desktop information architecture.
- [x] 8.2 Preserve supporting comments, mentions, replies, attachments, reactions, labels, properties, views, pins, quick actions, realtime, Agent builder, MCP, environment, provider, plugin, Git/VCS, channel, and Webhook workflows.
- [x] 8.3 Run every feature-parity row through create/configure, persistence, read/edit, execution, state projection, actionable failure, and restart recovery as applicable.
- [x] 8.4 Keep Web, Mobile, and docs source unless a scoped shared-contract edit is required; verify affected workspaces compile.

## 9. Remove Only Cloud-Control-Plane Runtime Paths

- [x] 9.1 Remove local Desktop reachability of signup/login/verification, cloud workspace lifecycle, human membership/invitations/tenant ACL, subscription/billing/quota/sales, remote sync, managed cloud runtimes, and hosted telemetry upload.
- [x] 9.2 Remove PostgreSQL, Redis, S3, and hosted endpoint dependencies only after retained callers have moved to local adapters.
- [x] 9.3 Audit routes, configuration, dependencies, and runtime calls to prove no retained feature was removed or still reaches a removed interface.

## 10. Review and Freeze

- [x] 10.1 Run fresh/upgrade/restart/offline/concurrency/recovery acceptance and record exact item-level failures.
- [x] 10.2 Run affected TypeScript and Go tests, Desktop build/package checks, and the project-native full verification on the exact candidate.
- [x] 10.3 Run one `rv`; fix only in-scope findings and rerun affected checks.
- [x] 10.4 Validate and archive `local-desktop-sqlite-v2`, then freeze the local candidate without unrequested commit, push, promotion, or deployment.

## Verification Evidence

- `go test ./...`, `go vet ./internal/localworkbench ./cmd/local-server`, and `go test -race ./internal/localworkbench -count=1` pass on the final implementation.
- `pnpm typecheck` and `pnpm test -- --run` pass across the retained TypeScript workspaces; existing React `act`, hydration, and canvas warnings remain non-failing upstream test output.
- macOS arm64 Desktop package completes with the bundled local server and passes `codesign --verify --deep --strict`.
- The packaged server preserves the fixed operator and a created Project across multiple process restarts, reports `local/sqlite`, and creates the database with mode `0600`.
- `make check-worktree` was attempted twice but its Docker preflight could not pull `pgvector/pgvector:pg17` because CloudFront ended the transfer with EOF. Its EXIT trap incorrectly printed success, which is not counted; the constituent local Go/TypeScript/build/package checks above are the accepted local evidence.
