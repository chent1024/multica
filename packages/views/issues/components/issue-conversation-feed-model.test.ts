import { describe, expect, it } from "vitest";
import type { AgentTask, IssueSession } from "@multica/core/types";
import { buildIssueConversationMarkers } from "./issue-conversation-feed-model";

describe("buildIssueConversationMarkers", () => {
  it("orders stage boundaries without inserting task transcript rows", () => {
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
    expect(buildIssueConversationMarkers(session, [task]).map(({ id, type, stageStatus, runCount }) => ({ id, type, stageStatus, runCount }))).toEqual([
      { id: "stage:stage-2", type: "stage", stageStatus: "active", runCount: 0 },
    ]);
  });

  it("keeps the checkpoint compact even when a run has a result", () => {
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

    const marker = buildIssueConversationMarkers(session, [task]).find(({ id }) => id === "stage:stage-2");

    expect(marker).toMatchObject({
      stageStatus: "active",
      runCount: 0,
    });
    expect(marker).not.toHaveProperty("latestOutcome");
  });

  it("does not repeat the initial stage below the current-work strip", () => {
    const session = {
      id: "session", issue_id: "issue", workspace_id: "workspace", active_stage_id: "stage-1",
      continuity_state: "fresh", continuity_reason: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", handoff: null,
      stages: [
        { id: "stage-1", session_id: "session", kind: "analysis", status: "active", position: 0, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: null },
      ],
    } satisfies IssueSession;

    expect(buildIssueConversationMarkers(session, [])).toEqual([]);
  });
});
