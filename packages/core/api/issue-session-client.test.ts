import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { AgentTaskSchema, IssueSessionSchema } from "./schemas";

const session = {
  id: "session-1", issue_id: "issue-1", workspace_id: "ws-1", active_stage_id: "stage-1",
  continuity_state: "fresh", continuity_reason: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  stages: [{ id: "stage-1", session_id: "session-1", kind: "analysis", status: "active", position: 0, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: null }], handoff: null,
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
    expect(AgentTaskSchema.parse({ id: "run-1", stage_kind: "review", continuity_state: "gap" })).toMatchObject({ stage_kind: "review", continuity_state: "gap" });
  });
});
