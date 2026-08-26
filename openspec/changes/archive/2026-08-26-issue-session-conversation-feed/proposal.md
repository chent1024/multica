## Why

Issue detail already stores one durable session and renders comments across Runs, but the primary composer only adds a comment while Run creation is a separate action. Users therefore cannot reliably continue the same Issue conversation with one send, and stage changes and Run outcomes do not read as one coherent product narrative.

## What Changes

- Turn the Issue detail activity area into the canonical Issue Session conversation feed, interleaving human/agent comments with compact stage and Run state markers.
- Make the bottom composer submit one atomic continuation request that records the user message and creates one Run in the active stage.
- When a Run is already active, persist the new request as the next queued turn and promote it only after the current Run finishes.
- Link agent final responses to their source Run so the feed can show authoritative outcomes without exposing raw tool traffic.
- Keep task transcripts, raw tool calls, identifiers, token usage, and provider diagnostics in existing secondary inspectors.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `issue-session-workflow`: add atomic message-to-Run continuation, durable queued follow-up semantics, and source-linked agent outcomes.
- `issue-session-product-workspace`: make the main Issue narrative a continuous conversation feed with stage markers and one continuation composer.

## Impact

- SQLite task-run persistence and upgrade path.
- Local Workbench Issue Session and daemon task lifecycle APIs.
- Core API schemas, types, mutations, and React Query cache updates.
- Shared Issue detail conversation rendering, composer behavior, localization, and tests.
- No cloud service, account, permission, Git, deployment, or external-runtime authorization changes.
