## Purpose

Preserves retained OAuth, Webhook, VCS, chat-channel, plugin, and external integration behavior with explicit local credentials and callback reachability.

## ADDED Requirements

### Requirement: Outbound integrations use local configuration
Retained outbound integrations SHALL use locally stored configuration and credentials and SHALL expose actionable credential, network, provider, and response failures without requiring a hosted control plane.

#### Scenario: Provider rejects credentials
- **WHEN** an outbound integration receives an authentication failure
- **THEN** the relevant setup or execution surface shows the provider-specific actionable failure rather than a generic product error

### Requirement: Callback reachability is explicit
The local service SHALL expose exact loopback callback URLs and MAY use an explicitly configured public relay or tunnel base URL for Internet-originated Webhook and VCS callbacks; it SHALL NOT claim public reachability or provider execution capability when none exists.

#### Scenario: Integration requires a public callback
- **WHEN** no public relay or tunnel base URL is configured
- **THEN** setup remains visible and identifies callback reachability as the unmet prerequisite

### Requirement: Inbound delivery remains safe and idempotent
Retained inbound callbacks SHALL preserve signature or token validation, payload validation, deduplication, durable outcome recording, and documented response behavior through the local service.

#### Scenario: Provider retries a delivery
- **WHEN** a previously accepted delivery identity is received again
- **THEN** the handler returns the documented duplicate outcome without dispatching duplicate product work

### Requirement: Integration configuration survives restart
Non-secret metadata and protected local credential references SHALL remain associated with the retained integration after restart without synchronizing them to a hosted workspace.

#### Scenario: User restarts after configuring an integration
- **WHEN** the local credential store remains available
- **THEN** the integration restores its configuration and truthful reachability state
