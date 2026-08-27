# sqlite-persistence Specification

## Purpose
Makes a single transactional SQLite module the authoritative durable store for every retained Desktop capability while preserving upstream product interfaces.
## Requirements
### Requirement: SQLite is the only Desktop product database
The supported Desktop runtime SHALL create, read, update, execute, and recover every retained product workflow using a Desktop-owned SQLite database without PostgreSQL or another external database.

#### Scenario: External databases are absent
- **WHEN** Desktop starts with no PostgreSQL service or database connection string
- **THEN** retained product state and workflows remain fully usable through their existing client contracts

### Requirement: Schema initialization and upgrades are transactional
The persistence module SHALL own fresh initialization and immutable ordered forward upgrades through a schema-version ledger, SHALL create indexes with `CREATE INDEX IF NOT EXISTS`, and SHALL commit each upgrade completely or leave the prior schema and data intact.

#### Scenario: Upgrade statement fails
- **WHEN** any statement in a pending schema upgrade fails
- **THEN** that upgrade is not recorded and none of its partial schema or data changes remain committed

### Requirement: Relationships are enforced by application transactions
The persistence module SHALL validate relationships and perform dependent cleanup atomically in application transactions without database foreign keys, cascading deletes, or cascading updates.

#### Scenario: Parent deletion violates a retained invariant
- **WHEN** a retained parent object still has dependents that prevent deletion
- **THEN** deletion fails without modifying the parent or its dependents

### Requirement: Persistence preserves retained contracts
SQLite repositories SHALL store every authoritative field and support every query, ordering, filtering, concurrency, and error behavior required by retained upstream Desktop HTTP and WebSocket contracts.

#### Scenario: Retained workflow restarts
- **WHEN** Desktop restarts after a committed create, edit, execution, integration, or scheduling operation
- **THEN** the existing client contract reconstructs the same durable product state without an invented fallback value
