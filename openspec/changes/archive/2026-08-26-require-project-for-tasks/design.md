## Context

The local product retains one fixed `workspace_id` as a compatibility boundary, while `projects` own local-directory resources and Issues currently allow a nullable `project_id`. Creation paths, the reusable project picker, child creation, project deletion, and Run creation all currently accept or produce projectless Issues. See `proposal.md` for the product motivation and the capability specs for observable behavior.

## Goals / Non-Goals

**Goals:**
- Make project ownership a server-enforced Issue invariant for all new writes.
- Keep legacy databases startable and make ambiguous old rows repairable without inventing a repository.
- Use the existing Project, Issue, Issue Session, Run, React Query, draft, and picker owners.
- Keep the local task list useful as a cross-project aggregate.

**Non-Goals:**
- Physically remove the fixed workspace table, route prefix, query keys, or internal identifiers.
- Change `LOCAL-N` identifiers to project-scoped numbering.
- Support moving an established task or provider thread between repositories.
- Require every existing Project record to gain a new resource model or change Codex adapter behavior.

## Decisions

### Enforce ownership in the Store and request types

`CreateIssue` is the transactional owner of task creation, so it resolves the parent, inherits its project where appropriate, requires a resulting project, and validates the relationship inside the creation transaction. Update accepts a project only as a compare-and-set repair for a null legacy value; established ownership is immutable. Handlers and TypeScript request types make ordinary top-level creation explicit, while the Store remains the final authority.

This is preferred over UI-only validation because shortcuts, API callers, and child handoffs otherwise remain able to create invalid rows.

### Keep SQLite nullable only as a bounded legacy bridge

Fresh schema declares `project_id NOT NULL`. The forward upgrade backfills projectless rows when exactly one project exists in that fixed data space. Ambiguous rows remain nullable so older content stays readable and can be assigned once through the existing detail project picker. All new writes and Run creation enforce the stronger invariant.

Rebuilding an existing SQLite table to `NOT NULL` is intentionally deferred until no legacy null rows remain; failing startup or fabricating an Inbox project would be worse than a narrow compatibility bridge.

### Make project ownership immutable

A Project maps to a Git repository and an Issue Session can retain provider-thread and work-directory continuity. Silently changing the Issue's project would make the task identity disagree with prior execution evidence. Therefore established project ownership cannot be changed. A legacy null value can be assigned once, and child tasks inherit the same value.

Copying or migrating an Issue Session to another Project is a separate capability, not an update-field behavior.

### Protect project deletion transactionally

Project deletion checks for owned Issues in the same transaction and returns a conflict instead of setting `project_id` to null. Empty projects retain the current resource cleanup path.

### Put project selection at the creation boundary

Both creation modes query projects through the existing React Query options. Caller-provided project context wins; otherwise exactly one project is selected. Multiple projects require explicit choice. Zero projects replace submission with an existing navigation action to add a project. The reusable picker gains a required mode rather than duplicating a second picker.

### Remove workspace wording without rewriting compatibility routing

Local mode omits the existing sidebar group label while retaining its navigation items and fixed workspace-scoped paths. This removes the obsolete product concept without destabilizing tabs, routes, or query ownership.

## Risks / Trade-offs

- [Legacy databases can contain ambiguous null projects] → Keep those Issues readable, make their picker a one-time repair action, and block Run creation until repaired.
- [A UI caller may omit project context] → Make project required in ordinary request types and retain Store validation as the final guard.
- [Child creation can conflict with a supplied project] → Resolve the parent first and reject mismatches before any transactional writes.
- [Project deletion races with task creation] → Perform the ownership check and delete in one database transaction on the single SQLite connection.
- [Persisted create drafts can reference a deleted project] → Resolve draft values against the current project query and fall back only to an unambiguous single project.

## Migration Plan

1. Fresh databases use a non-null `issues.project_id` definition.
2. Existing databases backfill null rows only in fixed data spaces containing exactly one project and record the migration version transactionally.
3. Remaining ambiguous rows stay readable and can be assigned once; all execution and continuation commands reject them until then.
4. Rollback keeps assigned project identifiers as valid historical data; application code can revert without destructive data conversion.
