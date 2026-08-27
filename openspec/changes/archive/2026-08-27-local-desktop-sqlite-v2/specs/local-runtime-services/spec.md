## Purpose

Replaces Redis coordination with local modules whose interfaces express product responsibilities and whose restart-sensitive facts are durable in SQLite.

## ADDED Requirements

### Requirement: Durable local work uses atomic claims
Queues, Runs, automation occurrences, model requests, Skill requests, and other restart-sensitive work SHALL use SQLite-backed atomic claim and lease operations that prevent concurrent duplicate ownership.

#### Scenario: Two workers claim the same work
- **WHEN** two eligible local workers concurrently request one pending item
- **THEN** at most one receives the claim and the other observes no claimable copy

### Requirement: Scheduling is restart-safe and idempotent
Local schedule evaluation SHALL persist definitions, occurrence identities, claims, outcomes, and retryable failures so restart or concurrent evaluation cannot create duplicate execution for one planned occurrence.

#### Scenario: Desktop restarts around an occurrence
- **WHEN** an occurrence was already claimed or completed before restart
- **THEN** the resumed scheduler does not dispatch a second Run for the same occurrence identity

### Requirement: Ephemeral coordination remains truthful
Runtime liveness, short-lived locks, rate limits, caches, and in-process event fan-out SHALL use local time and process state, SHALL persist only facts requiring recovery, and SHALL not report stale cloud presence as local availability.

#### Scenario: Runtime disconnects
- **WHEN** a local runtime misses its liveness window or deregisters
- **THEN** dependent features retain configuration but expose the runtime as unavailable with an actionable dispatch failure

### Requirement: Realtime follows committed state
Local event fan-out SHALL publish only committed state transitions through the retained WebSocket contract and SHALL support reconnect/refetch without duplicate durable activity.

#### Scenario: Transaction rolls back
- **WHEN** a product write fails before commit
- **THEN** no success event is published and clients can observe the actionable failure
