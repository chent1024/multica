## ADDED Requirements
### Requirement: Provider availability is independent of daemon process liveness
A running desktop daemon SHALL NOT turn server-reported offline or unavailable provider runtimes online. Whole-daemon offline transitions SHALL continue to clear dependent availability.
#### Scenario: Mixed availability on one machine
- **WHEN** Codex is available while Claude and Pi are unavailable on the same running daemon
- **THEN** Codex remains online and the unavailable providers remain offline in native runtime details
### Requirement: CLI provenance uses daemon metadata
Runtime responses SHALL distinguish provider binary version from the Multica daemon CLI version and persist authoritative registration metadata. Legacy rows without CLI metadata SHALL report an unknown CLI version rather than copying a provider version.
#### Scenario: Different binary versions
- **WHEN** a provider reports its own version and the daemon registers a different Multica CLI version
- **THEN** machine CLI details show the daemon version and provider details retain the provider version
