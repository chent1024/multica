import { describe, expect, it } from "vitest";
import type { AgentTask, IssueSessionStage, IssueSessionStageKind, TimelineEntry } from "@multica/core/types";
import { buildIssueSessionWorkspaceModel, issueSessionStageKinds } from "./issue-session-workspace-model";

function stage(id: string, kind: IssueSessionStageKind, position: number, status: IssueSessionStage["status"] = "completed"): IssueSessionStage {
  return { id, session_id: "session-1", kind, status, position, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: status === "active" ? null : "2026-01-01T01:00:00Z" };
}

function task(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "run-1", agent_id: "agent-1", runtime_id: "runtime-1", issue_id: "issue-1", session_id: "session-1", stage_id: "active", stage_kind: "review",
    status: "completed", priority: 0, dispatched_at: null, started_at: null, completed_at: "2026-01-01T02:00:00Z",
    result: { summary: "Fallback run summary" }, error: null, created_at: "2026-01-01T01:00:00Z", ...overrides,
  };
}

describe("buildIssueSessionWorkspaceModel", () => {
  it.each([
    ["analysis", "implementation"], ["implementation", "review"], ["review", "test"],
    ["test", "commit"], ["commit", "push"], ["push", "release"], ["release", null],
  ] as const)("recommends %s → %s", (kind, expected) => {
    const model = buildIssueSessionWorkspaceModel([stage("active", kind, 0, "active")], "active", "resumed", [], []);
    expect(model?.recommendedNext).toBe(expected);
    expect(model?.availableStages).toEqual(issueSessionStageKinds.filter((candidate) => candidate !== kind));
  });

  it("preserves repeated occurrences in persisted position order", () => {
    const model = buildIssueSessionWorkspaceModel([
      stage("review", "review", 1), stage("implementation-2", "implementation", 2, "active"), stage("implementation-1", "implementation", 0),
    ], "implementation-2", "gap", [], []);
    expect(model?.occurrences.map(({ kind }) => kind)).toEqual(["implementation", "review", "implementation"]);
    expect(model?.hasContinuityGap).toBe(true);
    expect(model?.progressGroups).toEqual([
      { kind: "analysis", status: "upcoming" },
      { kind: "implementation", status: "active" },
      { kind: "validation", status: "upcoming" },
      { kind: "delivery", status: "upcoming" },
    ]);
  });

  it("does not invent an active stage for an invalid server pointer", () => {
    expect(buildIssueSessionWorkspaceModel([stage("analysis", "analysis", 0, "active")], "missing", "fresh", [], [])).toBeNull();
  });

  it("projects the newest exact-stage run and prefers its timeline outcome", () => {
    const tasks = [
      task({ id: "older", created_at: "2026-01-01T00:00:00Z" }),
      task({ id: "current", status: "running", completed_at: null, created_at: "2026-01-01T03:00:00Z" }),
      task({ id: "other-stage", stage_id: "other", created_at: "2026-01-01T04:00:00Z" }),
    ];
    const timeline: TimelineEntry[] = [{
      type: "comment", id: "comment-1", actor_type: "agent", actor_id: "agent-1", created_at: "2026-01-01T03:30:00Z",
      source_task_id: "current", content: "Review found no blocking issues.",
    }];

    const model = buildIssueSessionWorkspaceModel([stage("active", "review", 0, "active")], "active", "resumed", tasks, timeline);

    expect(model?.latestStageTask?.id).toBe("current");
    expect(model?.activeTask?.id).toBe("current");
    expect(model?.stageRunCount).toBe(2);
    expect(model?.latestOutcome).toBe("Review found no blocking issues.");
  });

  it("falls back to the structured run summary and never borrows another stage", () => {
    const model = buildIssueSessionWorkspaceModel(
      [stage("active", "test", 0, "active")],
      "active",
      "fresh",
      [task({ stage_id: "active", stage_kind: "test" }), task({ id: "other", stage_id: "other", error: "Wrong stage" })],
      [],
    );
    expect(model?.latestOutcome).toBe("Fallback run summary");
    expect(model?.stageRunCount).toBe(1);
  });
});
