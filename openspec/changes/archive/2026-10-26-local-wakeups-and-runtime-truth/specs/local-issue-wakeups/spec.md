## ADDED Requirements
### Requirement: Existing Wakeup APIs have durable local implementations
The local runtime SHALL implement the upstream issue and workspace Wakeup endpoints, including schedule and event/condition configuration, summaries, pagination/filter counts, disabled/paused/ended rules, run history, instruction edits, revision-aware enable/rearm, manual triggering, deletion, and child-done system rules and workspace defaults. It SHALL use existing local identity and workspace ownership and reject invalid cross-resource references.
#### Scenario: Configure and restart
- **WHEN** a local user creates, edits, disables or re-enables a rule and restarts Desktop
- **THEN** its configuration and lifecycle survive and existing upstream UI schemas parse the real record
#### Scenario: Invalid or stale management request
- **WHEN** a request contains an invalid target, invalid schedule/filter/condition, or stale revision
- **THEN** the runtime rejects it without changing the rule or unrelated state
### Requirement: Scheduling and event consumption are restart-safe
The local runtime SHALL support at/every/cron schedules and prospective event subscriptions, actor/run filters, local structured conditions, absolute/relative expiry and timeout actions, bounded repeating fires, and actionable failures. It SHALL atomically bind each dispatched occurrence to an existing local task run, retain source evidence, and prevent overlapping duplicate dispatch and self-trigger loops.
#### Scenario: Duplicate tick and restart
- **WHEN** multiple ticks or a restarted runtime observe the same due occurrence or captured event
- **THEN** at most one run is created and the same committed occurrence/history remains observable
#### Scenario: Rule disabled or issue closed
- **WHEN** a rule is disabled/deleted or its issue becomes terminal before a queued or claimed run starts
- **THEN** no new run starts from that rule and its lifecycle is represented truthfully
#### Scenario: Runtime is unavailable
- **WHEN** a due rule targets an unavailable harness
- **THEN** the rule retains recoverable pending work and an actionable error rather than claiming a successful execution
### Requirement: Child completion uses local persisted facts
The runtime SHALL support child-done defaults and per-issue overrides, stage-aware completion evidence, agent/squad dispatch or member notification, and idempotent delivery without re-firing the same completed stage.
#### Scenario: Child stage closes
- **WHEN** the applicable child stage completes with later stages waiting, or all children finish
- **THEN** the rule delivers one corresponding wakeup or notification and records evidence durably
### Requirement: User data remains intact on upgrade
The change SHALL append transactional SQLite migrations, preserve existing project/agent/issue/resource identities and payloads, and verify installation against a pre-upgrade snapshot with a full app/profile restore path.
#### Scenario: Upgrade fails
- **WHEN** an upgrade statement fails
- **THEN** the transaction rolls back and the old schema/data remain intact

#### Scenario: External pull request evidence is unavailable
- **WHEN** a linked pull request lacks a current-head integration snapshot or its provider cannot report the requested mergeability fact
- **THEN** the condition remains pending with an actionable diagnostic and retains its prior evidence; no successful condition is invented
