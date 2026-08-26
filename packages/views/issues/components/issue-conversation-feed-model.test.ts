import { describe, expect, it } from "vitest";
import type { AgentTask, IssueSession } from "@multica/core/types";
import { buildIssueConversationMarkers } from "./issue-conversation-feed-model";

describe("buildIssueConversationMarkers", () => {
  it("orders stage boundaries and run state without task transcript messages", () => {
    const session = {
      id: "session", issue_id: "issue", workspace_id: "workspace", active_stage_id: "stage-2",
      continuity_state: "pending", continuity_reason: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z", handoff: null,
      stages: [
        { id: "stage-1", session_id: "session", kind: "analysis", status: "completed", position: 0, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: "2026-01-02T00:00:00Z" },
        { id: "stage-2", session_id: "session", kind: "implementation", status: "active", position: 1, migrated: false, started_at: "2026-01-02T00:00:00Z", completed_at: null },
      ],
    } satisfies IssueSession;
    const task = {
      id: "run-1", agent_id: "agent", runtime_id: "runtime", issue_id: "issue", session_id: "session", stage_id: "stage-1", status: "waiting_previous_run",
      priority: 0, dispatched_at: null, started_at: null, completed_at: null, result: null, error: null, created_at: "2026-01-01T00:00:01Z",
    } satisfies AgentTask;
    expect(buildIssueConversationMarkers(session, [task]).map(({ id, type, status, stageStatus, runCount }) => ({ id, type, status, stageStatus, runCount }))).toEqual([
      { id: "stage:stage-1", type: "stage", status: undefined, stageStatus: "completed", runCount: 1 },
      { id: "run:run-1", type: "run", status: "waiting_previous_run", stageStatus: undefined, runCount: undefined },
      { id: "stage:stage-2", type: "stage", status: undefined, stageStatus: "active", runCount: 0 },
    ]);
  });

  it("uses the latest exact-stage agent outcome in a completed checkpoint", () => {
    const session = {
      id: "session", issue_id: "issue", workspace_id: "workspace", active_stage_id: "stage-2",
      continuity_state: "resumed", continuity_reason: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z", handoff: null,
      stages: [
        { id: "stage-1", session_id: "session", kind: "analysis", status: "completed", position: 0, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: "2026-01-02T00:00:00Z" },
        { id: "stage-2", session_id: "session", kind: "implementation", status: "active", position: 1, migrated: false, started_at: "2026-01-02T00:00:00Z", completed_at: null },
      ],
    } satisfies IssueSession;
    const task = {
      id: "run-1", agent_id: "agent", runtime_id: "runtime", issue_id: "issue", session_id: "session", stage_id: "stage-1", status: "completed",
      priority: 0, dispatched_at: null, started_at: null, completed_at: "2026-01-01T01:00:00Z", result: { summary: "Fallback" }, error: null, created_at: "2026-01-01T00:00:01Z",
    } satisfies AgentTask;

    const marker = buildIssueConversationMarkers(session, [task], [{
      id: "result", type: "comment", actor_type: "agent", actor_id: "agent", source_task_id: "run-1",
      content: "Scope and acceptance criteria confirmed.", created_at: "2026-01-01T01:00:00Z",
    }]).find(({ id }) => id === "stage:stage-1");

    expect(marker).toMatchObject({
      stageStatus: "completed",
      runCount: 1,
      latestOutcome: "Scope and acceptance criteria confirmed.",
    });
  });
});
