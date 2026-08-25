import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { AgentTaskSchema, IssueSessionContinuationSchema, IssueSessionSchema } from "./schemas";

const session = {
  id: "session-1", issue_id: "issue-1", workspace_id: "ws-1", active_stage_id: "stage-1",
  continuity_state: "fresh", continuity_reason: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  stages: [{ id: "stage-1", session_id: "session-1", kind: "analysis", status: "active", position: 0, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: null }], handoff: null,
};

const issue = {
  id: "issue-1", workspace_id: "ws-1", number: 1, identifier: "MUL-1", title: "Ship it", description: null,
  status: "in_progress", priority: "none", assignee_type: "agent", assignee_id: "agent-1", creator_type: "member",
  creator_id: "member-1", parent_issue_id: null, project_id: null, position: 1, stage: null, start_date: null,
  due_date: null, metadata: {}, properties: {}, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
};

const task = {
  id: "run-1", agent_id: "agent-1", runtime_id: "runtime-1", issue_id: "issue-1", status: "queued", priority: 0,
  dispatched_at: null, started_at: null, completed_at: null, result: null, error: null, created_at: "2026-01-01T00:00:00Z",
};

afterEach(() => vi.unstubAllGlobals());

describe("issue session client contract", () => {
  it("validates reads and sends bounded stage activation", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(session), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    await expect(client.getIssueSession("issue-1")).resolves.toMatchObject({ id: "session-1" });
    await client.activateIssueSessionStage("issue-1", "review");
    expect(fetchMock.mock.calls[1]?.[0]).toBe("https://api.example.test/api/issues/issue-1/session/stages");
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body))).toEqual({ kind: "review" });
  });

  it("rejects malformed session data and accepts additive task metadata", () => {
    expect(() => IssueSessionSchema.parse({ ...session, active_stage_id: 7 })).toThrow();
    expect(IssueSessionSchema.parse({
      ...session,
      handoff: {
        id: "handoff-1", child_issue_id: "issue-1", parent_issue_id: "parent-1", parent_session_id: "parent-session",
        source_stage_id: "parent-stage", source_stage_kind: "analysis", source_run_id: null, objective: "Continue the child",
        confirmed_facts: [], decisions: [], constraints: [], acceptance_criteria: [], references: [], open_questions: [], created_at: "2026-01-01T00:00:00Z",
      },
    }).handoff?.source_stage_kind).toBe("analysis");
    expect(AgentTaskSchema.parse({ id: "run-1", stage_kind: "review", continuity_state: "gap" })).toMatchObject({ stage_kind: "review", continuity_state: "gap" });
  });

  it("continues the issue session and validates the authoritative issue and task", async () => {
    const payload = { outcome: "queued", issue, task };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(payload), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(client.continueIssueSession("issue-1")).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/issues/issue-1/session/continue",
      expect.objectContaining({ method: "POST" }),
    );
    expect(() => IssueSessionContinuationSchema.parse({ ...payload, outcome: "unknown" })).toThrow();
  });
});
