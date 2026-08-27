## Purpose

Makes Electron own a private loopback Go service and its data paths, lifecycle, health, and packaging.

## ADDED Requirements

### Requirement: Product service is loopback-only
The Desktop product service SHALL listen only on a loopback interface and SHALL reject product requests that do not originate from loopback while preserving valid local uploads and configured callback handling.

#### Scenario: Non-loopback product request arrives
- **WHEN** a request reaches a product route from a non-loopback peer
- **THEN** the server rejects it before executing product behavior

### Requirement: Electron owns service lifecycle
Electron SHALL choose isolated port and data paths, start the correct local server binary, wait for a health and identity handshake, reconnect clients after a recoverable crash, and terminate the child during shutdown.

#### Scenario: Local server crashes
- **WHEN** the child exits unexpectedly while Desktop remains open
- **THEN** Desktop reports the interruption, performs its bounded restart policy, and reconnects only after the expected local identity is healthy

### Requirement: Development and packaged Desktop share the contract
Development and packaged Desktop SHALL start the same local service interface with checkout or installation-isolated database and file roots and SHALL NOT require PostgreSQL, Redis, S3, or a hosted product endpoint.

#### Scenario: Packaged Desktop starts offline
- **WHEN** a packaged application launches without Internet access or external infrastructure
- **THEN** the bundled service becomes healthy and retained offline-capable workflows remain usable
