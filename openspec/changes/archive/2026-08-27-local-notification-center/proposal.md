## Why

The localized Desktop still presents an upstream collaboration-oriented “Inbox”, but its only local producer writes incomplete Agent terminal rows and does not publish the retained realtime notification contract. A single-operator product needs a truthful local notification center for events that require attention, without recreating member, invitation, mention, subscription, or reaction workflows.

## What Changes

- Rename the user-visible Inbox surface to Notifications while retaining the existing internal route, API, query keys, and SQLite table names for compatibility.
- Define one local notification contract with supported types, severity, actor, source identity, actionable destination, realtime publication, read/archive state, restart recovery, and idempotent creation.
- Notify the local operator about Agent review requests, Agent failures or blocked work, quick-create outcomes, automation failures, integration or Webhook failures, and actionable Runtime availability or credential failures.
- Keep ordinary successful completion informational only when it produces a review request or another user-visible result; do not notify the operator about their own routine edits.
- Remove collaboration-only assignment, member comment, mention, subscription, reaction, invitation, and workspace-switching notification choices from the local Desktop surface and defaults.
- Simplify local preferences to all notifications, action-required only, or muted system banners without deleting shared Web or Mobile source contracts.

## Capabilities

### New Capabilities

- `local-notification-center`: Durable, realtime, actionable, deduplicated notifications for the single-operator local Desktop.

### Modified Capabilities

- `retained-feature-parity`: Rename the retained user-facing Inbox surface to Notifications and constrain its localized value to non-collaboration events.

## Impact

- Local SQLite schema and migration ownership in `server/internal/localworkbench`.
- Local Agent, quick-create, automation, integration, Webhook, plugin, and Runtime transition handlers.
- Retained `/api/inbox*`, notification-preference, WebSocket, React Query, native notification, and Desktop navigation contracts.
- Shared Inbox views and locale resources, with internal identifiers preserved to avoid an unrelated API or route migration.
