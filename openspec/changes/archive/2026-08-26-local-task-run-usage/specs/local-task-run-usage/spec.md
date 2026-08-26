## Purpose

Record truthful per-Run Codex token consumption locally and let users inspect Run and Issue usage without introducing cloud billing or account concepts.

## ADDED Requirements

### Requirement: Run usage snapshots are durable and idempotent
The local workbench SHALL accept a complete usage snapshot for an existing Run in the fixed local data space. Each snapshot SHALL contain at most one row for each provider and model pair, SHALL reject blank provider or model values and negative token or cost counters, and SHALL replace all previously recorded rows for that Run transactionally. Repeating the same snapshot SHALL produce the same stored result, and an empty snapshot SHALL remove all previously recorded usage for the Run.

#### Scenario: Daemon reports a multi-model snapshot
- **WHEN** the daemon reports valid usage rows for two different provider and model pairs on an existing local Run
- **THEN** both rows are stored as that Run's complete usage snapshot

#### Scenario: Daemon repeats a snapshot
- **WHEN** the daemon repeats an already accepted usage snapshot
- **THEN** the Run contains exactly one row per reported provider and model pair with unchanged counters

#### Scenario: Daemon replaces a snapshot
- **WHEN** the daemon reports a later valid snapshot for the same Run with different rows or counters
- **THEN** all prior rows for the Run are atomically replaced by the later snapshot

#### Scenario: Daemon clears a snapshot
- **WHEN** the daemon reports an empty usage snapshot for an existing Run
- **THEN** the Run has no recorded usage rows

#### Scenario: Snapshot is invalid or out of scope
- **WHEN** a snapshot has duplicate provider and model pairs, a blank provider or model, a negative counter, or names a Run outside the fixed local data space
- **THEN** the request is rejected without changing any previously recorded usage

### Requirement: Terminal outcomes retain their consumed usage
The daemon SHALL attempt to report accumulated usage after the agent process returns and before it reports the Run's terminal outcome. Completed, failed, and cancelled Runs SHALL all be eligible to retain usage. Transient report failures SHALL receive a short bounded retry, while permanent client errors SHALL not be retried and usage retry SHALL not use the terminal callback's multi-minute retry window.

#### Scenario: Agent fails after consuming tokens
- **WHEN** an agent returns a failed result with accumulated usage
- **THEN** the daemon reports the usage before reporting the failed Run outcome

#### Scenario: Running agent is cancelled
- **WHEN** cancellation interrupts an agent after it has accumulated usage
- **THEN** the daemon reports the returned usage snapshot even though the Run is already or is about to become cancelled

#### Scenario: Usage report encounters a transient error
- **WHEN** the usage report encounters a transport error, HTTP 408, HTTP 429, or HTTP 5xx response
- **THEN** the daemon retries within the dedicated short bounded schedule

#### Scenario: Usage report encounters a permanent error
- **WHEN** the usage report receives another HTTP 4xx response
- **THEN** the daemon stops retrying and continues to the terminal Run callback

### Requirement: Existing Issue Run interfaces expose recorded usage
The existing Issue task-runs interface SHALL return each Run's own recorded usage as provider and model rows. A Run with no recorded rows SHALL return an empty usage array, while an older or incompatible server may still be interpreted by the client as unknown. The existing Issue usage interface SHALL return sums for all recorded token buckets and the total number of Runs belonging to the Issue.

#### Scenario: Issue has Runs with and without usage
- **WHEN** the client lists an Issue whose Runs include recorded and unrecorded usage
- **THEN** each recorded Run includes its exact rows and each unrecorded Run includes an empty usage array

#### Scenario: Issue usage is requested
- **WHEN** the client requests usage for an Issue in the fixed local data space
- **THEN** the response contains summed input, output, cache-read, and cache-write counters and the Issue's Run count

#### Scenario: Issue is outside the local data space
- **WHEN** either Issue usage interface is requested for an Issue outside the fixed local data space
- **THEN** the request returns not found without exposing its Runs or usage

### Requirement: Issue deletion removes dependent usage explicitly
Deleting an Issue SHALL explicitly remove every usage row belonging to its Runs in the same application transaction, without database foreign keys or cascading actions.

#### Scenario: User deletes an Issue with recorded usage
- **WHEN** the Issue deletion transaction succeeds
- **THEN** no usage row remains for any deleted Run

### Requirement: Issue usage inspection uses local token semantics
The Issue execution-log usage entry and detail inspector SHALL present total processed tokens, input cache hit rate, output tokens, Run count, cumulative completed duration, and per-Run token details. Input cache hit rate SHALL be calculated from aggregated counters as cache-read tokens divided by the sum of uncached input and cache-read tokens. The Issue usage surface SHALL NOT present cloud cost, cache savings in currency, account quota, billing, subscription, or per-agent spend analysis.

#### Scenario: Issue contains differently sized Runs
- **WHEN** the inspector aggregates Runs with different input sizes
- **THEN** it sums uncached input and cache-read counters before calculating one weighted cache hit rate

#### Scenario: Historical Run has no recorded usage
- **WHEN** a Run has no recorded usage rows
- **THEN** the Run remains represented as unknown rather than as zero token consumption

#### Scenario: User opens token details
- **WHEN** the Issue has at least one Run with recorded usage and the user opens its usage inspector
- **THEN** the inspector shows Run status, trigger or time context, stage, model, uncached input, cache read, cache hit rate, output, total processed tokens, and duration
- **AND** it shows no currency or billing-oriented analysis

#### Scenario: Cache denominator is zero
- **WHEN** aggregated uncached input and cache-read tokens are both zero
- **THEN** the cache hit rate is shown as unavailable rather than as a fabricated zero-percent measurement
