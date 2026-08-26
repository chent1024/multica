## Context

The local Workbench already has one SQLite-backed Issue Session per Issue, ordered stage occurrences, task Runs, a unified comment/activity timeline, and a rich Issue composer. Run transcripts are a separate technical surface. The missing seam is an atomic user-turn command and a projection that can interleave existing records without duplicating persistence.

## Goals / Non-Goals

**Goals:**

- Preserve one durable Issue Session while making each composer send one durable conversational turn.
- Serialize follow-up Runs for the local Codex runtime and make waiting visible.
- Reuse comments, stages, Runs, timeline caching, and the existing composer.
- Keep old databases and historical comments/Runs readable.

**Non-Goals:**

- Injecting text into an already-running provider process.
- A new chat/session table, workflow engine, or generalized queue framework.
- Moving raw task messages or operational diagnostics into the main feed.
- Any automatic stage transition, Git delivery, deployment, or release.

## Decisions

### Use a transactional Issue Session message command

Add an Issue-scoped endpoint that accepts message content, attachments, and an idempotency key. In one SQLite transaction it validates the Issue/runtime, aligns a parked Issue when permitted, inserts the user comment, binds attachments, and inserts the Run. The response returns authoritative comment, Issue, Run, and `queued` or `waiting` outcome.

This is preferred over chaining the existing comment and continuation endpoints because either request can fail independently and retries can duplicate one side. Ordinary comments and replies retain their existing endpoint and never start work.

### Store request identity and trigger linkage on task_runs

Add nullable `request_key` and `trigger_comment_id` columns plus a partial unique request-key index. This extends the existing Run aggregate instead of introducing a shadow conversation model. Historical rows remain null. The request key is the retry identity; the trigger comment links a queued/waiting Run to its visible user turn.

### Represent serialized follow-up with one bounded Run status

Use `waiting_previous_run` for a persisted follow-up that cannot yet be claimed. Daemon claim continues to select only `queued`. On terminal completion/failure/cancellation, the same transaction promotes the oldest waiting Run for that Issue, recalculates its prior pointer and continuity evidence, and leaves later waiting Runs untouched. This avoids provider-process injection and preserves max-one execution.

### Build the feed as a client projection over authoritative records

Do not add a feed table. The main column merges existing timeline entries, stage occurrences, and Runs into stable chronological feed items. Comments retain the existing virtualized rendering and reply behavior; stage/Run markers are compact product annotations. Agent completion comments gain `source_task_id`, allowing response-to-Run association. The existing transcript dialog and execution log remain secondary.

### Reuse the rich composer with a different top-level submit mutation

The bottom `CommentInput` keeps drafts, uploads, keyboard behavior, and attachment gating. Only its top-level submit callback changes to the Issue Session message mutation. Reply inputs continue creating ordinary comments so a reply does not unexpectedly execute work.

## Risks / Trade-offs

- [A waiting Run may outlive a failed predecessor] → Promotion recalculates the latest usable pointer and records a continuity gap when none exists.
- [A crash can occur after a daemon terminal retry] → Terminal updates and waiting promotion are idempotent within one database transaction.
- [Merging three projections can reorder equal timestamps] → Use deterministic type priority and stable identifiers after timestamp order.
- [Long feeds can regress rendering] → Preserve the existing timeline virtualization; feed markers are lightweight rows and task messages are not loaded.
- [Older databases lack new columns] → The existing startup upgrader adds nullable columns and index without rewriting historical rows.

## Migration Plan

1. Extend fresh schema and startup upgrade with nullable Run linkage columns and the unique request-key index.
2. Deploy backend transaction and promotion logic before switching the composer.
3. Add core schemas/mutations and feed projection, then enable the new Issue detail behavior.
4. Rollback remains compatible: older code ignores nullable columns and `waiting_previous_run` rows stay unclaimed; rolling forward restores promotion. No destructive down-migration is required.
