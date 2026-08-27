# Upstream Feature-Parity Matrix

Baseline: `09a2410e882be8435bd6c4a26e03f7e288038203`

Rule: `keep` is the default. A capability can be marked `remove` only when its user value is exclusively cloud identity, cloud multi-tenancy, subscription/billing, remote synchronization, or hosted infrastructure. Removing a backend dependency does not authorize removing its product behavior; retained behavior must receive a local implementation.

## Desktop Navigation and Primary Workflows

| Capability | Decision | Local contract | Acceptance evidence |
| --- | --- | --- | --- |
| Search and command palette | keep | Searches retained local objects and opens every retained page | Search result navigation plus empty/failure state |
| New task | keep | Creates a Project-owned local Issue and can start a selected Agent/Squad | Create, persist, dispatch, restart |
| Inbox | keep | Projects locally generated actionable events for the local operator | Event creation, read state, navigation, restart |
| Chat | keep | Local conversations with configured Agents | Send, pending/failure, Agent result, restart |
| My Issues | keep/localize | Uses the local operator/system identity instead of a cloud member | List/filter/navigation with local assignments |
| Issues | keep | Full list, board, table, views, filters, grouping and task workflow | CRUD, move, batch, views, execution, failures |
| Projects | keep | Local Git Projects, resources and project task views | CRUD, resources, occupied delete guard |
| Automations | keep/localize | All upstream trigger types, targets, run history, deliveries and replay operate locally; hosted scheduling is replaced | CRUD, manual, cron, event, Webhook, history, replay, restart |
| Agents | keep/localize | Full configurable local Agent management without cloud ownership/ACL | Manual/AI create, edit, archive, skills, MCP, env, tasks |
| Squads | keep/localize | Agent-only local coordination without human-team permissions | CRUD, membership, leader, delegation, activity |
| Statistics | keep/localize | Run/usage/failure/runtime aggregates from SQLite without billing | Reconciled totals, filters, unknown usage, restart |
| Runtimes | keep/localize | Local daemon machines, providers, models, Skills and activity | Register, update, inspect, model/Skill request, delete guards |
| Skills | keep/localize | Full local Skill library and Agent assignment | Create/import/search/edit/files/refresh/assign/execute |
| Settings | keep/localize | Preserve every non-cloud setting; replace account/workspace pages with local equivalents only where needed | Read/write/restart per retained setting |

## Supporting Product Capabilities

| Capability | Decision | Local contract | Acceptance evidence |
| --- | --- | --- | --- |
| Comments, mentions and replies | keep/localize | Member mentions become local operator references; Agent/Squad/Issue mentions remain functional | Create/edit/delete/resolve/reply/trigger |
| Attachments and preview | keep | Desktop-owned local files with compatible metadata/content routes | Upload/preview/download/delete/restart |
| Reactions | keep | Local operator and Agent reactions remain durable | Add/remove/reload |
| Labels, properties and metadata | keep | Full local customization and assignment | CRUD, attach/detach, filter/restart |
| Issue views and preferences | keep | Saved local views and ordering | CRUD, activate, preference restart |
| Pins and quick actions | keep | Local pinned objects and executable quick actions | CRUD/reorder/render/run |
| Realtime events | keep/localize | Local WebSocket invalidation and activity projection | Same-process and reconnect behavior |
| Agent builder | keep/localize | AI/manual creation uses local runtime and credentials | Session, draft, model switch, completion/failure |
| MCP, environment and provider settings | keep | Persisted locally and applied to Runs without cloud secrets | Configure, redact, execute, restart |
| Plugins and plugin hooks | keep/localize | Installed local plugins, packages, storage and explicitly invoked hooks work without hosted publishing | Install/configure/invoke/restart |
| Git/VCS integration | keep/localize | Uses local repository and locally supplied credentials | Connect/status/task execution/failure |
| Slack, Lark, WeCom, DingTalk and Telegram | keep source/surface | Preserve settings and source; local credential records remain manageable, while provider-hosted or unported inbound/outbound engines report unavailable and are not advertised as active | Surface, configure record, explicit unavailable state, delete/restart |
| Webhooks | keep/localize | Local server accepts documented trigger/delivery requests; hosted relay is not required | Token/signature, dedupe, trigger, response, replay |
| Desktop updates and diagnostics | keep | Preserve update, crash containment and local diagnostics; cloud telemetry upload is separately classified | Check/update errors, crash recovery, diagnostics export |
| Onboarding | keep/localize | Guides local runtime, Project and Agent setup without signup or cloud workspace creation | Fresh-install completion and restart |
| Web and Mobile source | keep source | Not deleted as a localization shortcut; changes only when shared contracts require compilation fixes | Workspace builds/typechecks where affected |
| Documentation source | keep/localize | Retained feature docs remain; cloud-only setup text is separated from local Desktop guidance | Link and build checks where affected |

## Cloud-Only Runtime Capabilities Removed from Local Desktop

| Capability | Decision | Direct cloud evidence | Local replacement |
| --- | --- | --- | --- |
| Account signup/login/verification | remove from local runtime | Establishes hosted user identity | Deterministic local operator/system actor |
| Cloud workspace create/switch/delete | remove from local runtime | Multi-tenant hosted container lifecycle | One fixed local compatibility data space |
| Human members, invitations and tenant ACL | remove from local runtime | Requires multiple hosted identities and membership authorization | Single local operator; Agent/Squad permissions become local invariants |
| Subscription, billing, sales and cloud quota | remove from local runtime | Commercial hosted-service control plane | No billing gate; local usage statistics remain |
| Remote database and cache services | replace | PostgreSQL/Redis are hosted/shared infrastructure dependencies | Desktop-owned transactional SQLite and local event/scheduler modules |
| Remote workspace synchronization | remove from local runtime | Synchronizes state between hosted clients/devices | One local SQLite state owner |
| Managed cloud runtimes | remove from local runtime | Provisions and bills hosted execution machines | User-managed local runtimes remain complete |
| Hosted telemetry upload | remove/disable locally | Sends product data to a remote analytics service | Local diagnostics and crash containment remain |

## Complete-Use Acceptance Chain

Every `keep` or `keep/localize` row is incomplete until evidence covers all applicable steps:

1. Entry point remains visible and navigable.
2. Create or configure succeeds through the real Desktop/client contract.
3. SQLite stores the complete authoritative state transactionally.
4. Read, search, filter, edit and delete/archive behavior remains correct.
5. Execution or integration side effects run through the real local path.
6. WebSocket/refetch projections show committed state without stale cloud caches.
7. Validation and runtime failures present actionable item-level reasons.
8. Restart/reconnect recovers durable state and does not duplicate work.
9. Highest-level affected tests run without PostgreSQL, Redis, Internet, or hidden hosted fallbacks.
