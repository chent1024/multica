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
    expect(buildIssueConversationMarkers(session, [task]).map(({ id, type, status }) => ({ id, type, status }))).toEqual([
      { id: "stage:stage-1", type: "stage", status: undefined },
      { id: "run:run-1", type: "run", status: "waiting_previous_run" },
      { id: "stage:stage-2", type: "stage", status: undefined },
    ]);
  });
});
