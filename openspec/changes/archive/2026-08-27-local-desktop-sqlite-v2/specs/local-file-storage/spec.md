## Purpose

Preserves every retained file workflow through atomic Desktop-owned storage without requiring S3-compatible infrastructure.

## ADDED Requirements

### Requirement: Retained blobs use contained local storage
Attachments, avatars, artifacts, plugin files, and retained integration payload files SHALL be stored under one configured Desktop data root using normalized relative identities; reads and deletes SHALL reject paths outside that root.

#### Scenario: File identity attempts traversal
- **WHEN** a request resolves outside the configured storage root
- **THEN** the operation is rejected without reading, writing, or deleting that path

### Requirement: File writes are atomic
The storage adapter SHALL write incomplete content to a temporary file and expose the final identity only after the complete content has been safely renamed into place.

#### Scenario: Upload is interrupted
- **WHEN** writing a retained blob fails before completion
- **THEN** no final file or successful metadata record is exposed and temporary content is recoverably cleaned

### Requirement: Upstream file behavior remains compatible
Retained upload, metadata, preview, streaming download, replacement, and deletion workflows SHALL preserve their existing client-visible contract across restart without S3 credentials.

#### Scenario: User reopens an attachment after restart
- **WHEN** attachment metadata and content committed before shutdown
- **THEN** the normal retained preview or download route returns the same content and metadata
