## Purpose

Provide a durable local notification center that surfaces actionable results and failures from retained Desktop workflows without reintroducing cloud collaboration concepts.

## ADDED Requirements

### Requirement: Notifications cover retained local outcomes
The local Desktop SHALL create notifications for Agent work that is ready for review, failed, or blocked; quick-create outcomes; failed automation execution; failed integration, Webhook, or plugin execution; and Runtime or credential failures that require operator action. It SHALL NOT create notifications for the operator's routine edits or for removed assignment, member comment, mention, subscription, reaction, invitation, or workspace-switching events.

#### Scenario: Agent work is ready for review
- **WHEN** an Agent task commits a successful terminal result that moves its issue to review
- **THEN** the operator receives one informational review-request notification linked to that issue

#### Scenario: Retained local execution fails
- **WHEN** an Agent task, quick create, automation, integration, Webhook, plugin, or requested Runtime execution commits a failed or blocked outcome that requires operator action
- **THEN** the operator receives one action-required notification identifying the failed operation and its actionable reason

#### Scenario: Routine local edits do not notify
- **WHEN** the operator creates, edits, archives, or deletes ordinary local records without a background outcome requiring attention
- **THEN** the notification center does not create a self-notification for that edit

#### Scenario: Collaboration event is absent
- **WHEN** the local Desktop runs without members, invitations, mentions, subscriptions, or reactions
- **THEN** no collaboration-only notification type or preference is presented as a local capability

### Requirement: Notification creation is durable and idempotent
Every notification SHALL have a stable source identity within the local data space. Replaying the same committed outcome SHALL preserve exactly one notification, while different executions of the same workflow SHALL remain distinguishable. Read and archive state SHALL survive process and Desktop restarts.

#### Scenario: Terminal callback is replayed
- **WHEN** the same terminal execution outcome is received more than once
- **THEN** exactly one notification exists for that source identity

#### Scenario: A later execution fails independently
- **WHEN** the same automation or integration fails in two different execution attempts
- **THEN** each attempt has its own notification and source identity

#### Scenario: Desktop restarts
- **WHEN** the operator reads or archives a notification and restarts the local services and Desktop
- **THEN** the notification and its read or archived state are restored from local storage

### Requirement: Notifications publish after committed state
The local service SHALL publish the retained realtime notification event only after the notification and its source outcome have committed. The Desktop SHALL update unread state and the visible list without manual refresh, and SHALL reconcile from the API after reconnect.

#### Scenario: Notification commits
- **WHEN** a producer commits a newly inserted notification
- **THEN** one realtime notification event is published and the Desktop updates its unread projection and eligible system banner

#### Scenario: Producer transaction rolls back
- **WHEN** the source outcome or notification transaction does not commit
- **THEN** no realtime notification event is published

#### Scenario: Desktop reconnects after missing an event
- **WHEN** a committed notification was created while the Desktop realtime connection was unavailable
- **THEN** the next API reconciliation exposes the notification and correct unread count

### Requirement: Notification content leads to recovery
Each notification SHALL provide a supported type, valid severity, human-readable title, available concrete failure detail, and a safe local destination or recovery action. Issue-related notifications SHALL open the exact issue; configuration failures SHALL open the relevant local settings or retained activity surface. A notification SHALL NOT rely on an arbitrary external URL for navigation.

#### Scenario: Issue task fails with a concrete reason
- **WHEN** a failed Agent task reports a concrete error
- **THEN** the notification shows that error and opens the exact affected issue

#### Scenario: Configuration-backed execution fails
- **WHEN** an automation, integration, Webhook, plugin, Runtime, or credential operation fails outside an issue
- **THEN** the notification identifies the affected local object and opens its supported local recovery surface

### Requirement: Local notification preferences remain minimal
The local Desktop SHALL let the operator choose whether the notification list includes all supported notifications or only action-required notifications, and SHALL separately allow system banners to be muted. Shared Web or Mobile preference contracts MAY retain their collaboration fields, but the local Desktop SHALL neither display nor depend on them.

#### Scenario: Action-required filter is selected
- **WHEN** the operator selects action-required notifications
- **THEN** informational review or completion notifications are excluded from the local list while failure and blocked notifications remain visible

#### Scenario: System banners are muted
- **WHEN** the operator mutes system banners
- **THEN** notifications continue to persist and appear in the in-app list without producing operating-system banners

### Requirement: Internal Inbox identifiers remain compatible
The user-facing local Desktop SHALL label the capability Notifications while retaining the existing Inbox route, API paths, query keys, event name, and storage identifiers as internal compatibility details.

#### Scenario: Operator opens Notifications
- **WHEN** the operator selects Notifications in the local Desktop navigation
- **THEN** the existing internal Inbox route loads the local notification list without an API or storage migration of the internal feature name
