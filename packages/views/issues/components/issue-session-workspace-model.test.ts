import { describe, expect, it } from "vitest";
import type { IssueSessionStage, IssueSessionStageKind } from "@multica/core/types";
import { buildIssueSessionWorkspaceModel, issueSessionStageKinds } from "./issue-session-workspace-model";

function stage(id: string, kind: IssueSessionStageKind, position: number, status: IssueSessionStage["status"] = "completed"): IssueSessionStage {
  return { id, session_id: "session-1", kind, status, position, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: status === "active" ? null : "2026-01-01T01:00:00Z" };
}

describe("buildIssueSessionWorkspaceModel", () => {
  it.each([
    ["analysis", "implementation"], ["implementation", "review"], ["review", "test"],
    ["test", "commit"], ["commit", "push"], ["push", "release"], ["release", null],
  ] as const)("recommends %s → %s", (kind, expected) => {
    const model = buildIssueSessionWorkspaceModel([stage("active", kind, 0, "active")], "active", "resumed");
    expect(model?.recommendedNext).toBe(expected);
    expect(model?.availableStages).toEqual(issueSessionStageKinds.filter((candidate) => candidate !== kind));
  });

  it("preserves repeated occurrences in persisted position order", () => {
    const model = buildIssueSessionWorkspaceModel([
      stage("review", "review", 1), stage("implementation-2", "implementation", 2, "active"), stage("implementation-1", "implementation", 0),
    ], "implementation-2", "gap");
    expect(model?.occurrences.map(({ kind }) => kind)).toEqual(["implementation", "review", "implementation"]);
    expect(model?.hasContinuityGap).toBe(true);
  });

  it("does not invent an active stage for an invalid server pointer", () => {
    expect(buildIssueSessionWorkspaceModel([stage("analysis", "analysis", 0, "active")], "missing", "fresh")).toBeNull();
  });
});
