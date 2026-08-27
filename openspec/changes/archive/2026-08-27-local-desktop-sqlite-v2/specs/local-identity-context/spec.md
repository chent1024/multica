## Purpose

Replaces hosted authentication and multi-tenant membership with one deterministic local operator and one workspace-compatible data space without changing retained product callers.

## ADDED Requirements

### Requirement: Desktop bootstraps a fixed local identity context
The Desktop product SHALL enter one deterministic local operator and one deterministic workspace-compatible data space without signup, login, verification, token refresh, tenant selection, membership, or invitation operations.

#### Scenario: User launches Desktop offline
- **WHEN** Desktop starts without Internet connectivity or cloud credentials
- **THEN** bootstrap returns the fixed local context and every retained local route can resolve its workspace-shaped compatibility input

### Requirement: Identity compatibility does not restore cloud authorization
Workspace-shaped paths, headers, query keys, and records SHALL remain compatibility inputs only and SHALL NOT cause local product behavior to depend on tenant ownership, member ACL, subscriptions, billing, or quotas.

#### Scenario: Retained object is opened
- **WHEN** an object belongs to the fixed local data space
- **THEN** the local operator can use its retained workflow without a membership or entitlement lookup

### Requirement: Local authored activity has stable attribution
User-authored activity SHALL use the deterministic local operator while Agent and Squad execution actors remain distinct and durable.

#### Scenario: User and Agent both add activity
- **WHEN** the local operator comments and an Agent later reports execution progress
- **THEN** the timeline preserves distinct stable actor identities across restart
