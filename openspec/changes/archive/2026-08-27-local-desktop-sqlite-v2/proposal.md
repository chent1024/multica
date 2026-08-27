## Why

Multica's upstream product behavior is already complete; the localization should not redesign or reduce it. Replace only the cloud identity and infrastructure seams so the same product runs from Electron against local state and services.

## What Changes

- **BREAKING** Replace cloud authentication, tenant selection, membership context, invitations, subscriptions, billing, and quotas with one deterministic local operator and one fixed compatibility workspace.
- **BREAKING** Replace PostgreSQL persistence with a transactional SQLite Adapter while preserving retained HTTP, WebSocket, client, domain, and UI interfaces.
- Replace Redis-backed queues, claims, locks, liveness, caches, rate limits, pub/sub, and scheduling with local process modules and SQLite durability where restart safety is required.
- Use atomic local file storage for attachments, avatars, artifacts, and other retained blobs instead of requiring S3-compatible storage.
- Make Electron own the loopback API/WebSocket service lifecycle, database path, file path, port, shutdown, restart, and packaged binaries.
- Preserve Webhook, VCS, plugin, and other locally executable integrations through local credentials and explicitly configured callback reachability. Keep external-provider integration source and settings surfaces, but report provider-hosted or unported execution paths as unavailable instead of advertising false local capability.
- Preserve every upstream product feature and interaction not exclusively defined by the removed cloud account/control plane. Do not hide, reduce, or replace retained capabilities to make localization easier.

## Capabilities

### New Capabilities

- `local-identity-context`: Fixed local operator and workspace compatibility context replacing cloud authentication and membership.
- `sqlite-persistence`: Transactional SQLite persistence and forward upgrades covering all retained product state.
- `local-runtime-services`: Local replacements for Redis coordination, scheduling, liveness, cache, event, and rate-limit responsibilities.
- `local-file-storage`: Atomic desktop-owned storage for every retained file and artifact workflow.
- `loopback-desktop-runtime`: Electron ownership of the local API/WebSocket process and its lifecycle.
- `local-integration-reachability`: Local callback, Webhook, OAuth, and integration reachability without a hosted control plane.
- `retained-feature-parity`: Complete upstream behavior remains available through the local Adapters.

### Modified Capabilities

None. This upstream baseline did not contain an OpenSpec root before this change.

## Impact

- `apps/desktop`: owns local service bootstrap and lifecycle while keeping upstream product routing and interaction.
- `packages/core`, `packages/views`, and `packages/ui`: interfaces remain stable except where cloud-only identity presentation is removed.
- `server`: introduces local Adapters at identity, persistence, coordination, storage, and reachability seams instead of rebuilding product features as separate local variants.
- Packaging and local development stop requiring PostgreSQL, Redis, S3, or a hosted endpoint.
- Existing cloud databases are not migrated. The local product starts from or upgrades only its own SQLite database; recovery is forward-only and destructive down migrations are not introduced.
