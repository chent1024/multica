import type { IssueSessionStage, IssueSessionStageKind } from "@multica/core/types";

export const issueSessionStageKinds: readonly IssueSessionStageKind[] = [
  "analysis",
  "implementation",
  "review",
  "test",
  "commit",
  "push",
  "release",
];

const recommendedNextStage: Record<
  IssueSessionStageKind,
  IssueSessionStageKind | null
> = {
  analysis: "implementation",
  implementation: "review",
  review: "test",
  test: "commit",
  commit: "push",
  push: "release",
  release: null,
};

export interface IssueSessionWorkspaceModel {
  active: IssueSessionStage;
  recommendedNext: IssueSessionStageKind | null;
  availableStages: IssueSessionStageKind[];
  hasContinuityGap: boolean;
  occurrences: Array<{
    id: string;
    kind: IssueSessionStageKind;
    active: boolean;
    position: number;
  }>;
}

export function buildIssueSessionWorkspaceModel(
  stages: IssueSessionStage[],
  activeStageId: string,
  continuityState: "fresh" | "pending" | "resumed" | "gap",
): IssueSessionWorkspaceModel | null {
  const active = stages.find((stage) => stage.id === activeStageId);
  if (!active) return null;

  return {
    active,
    recommendedNext: recommendedNextStage[active.kind],
    availableStages: issueSessionStageKinds.filter(
      (kind) => kind !== active.kind,
    ),
    hasContinuityGap: continuityState === "gap",
    occurrences: stages
      .toSorted((left, right) => left.position - right.position)
      .map((stage) => ({
        id: stage.id,
        kind: stage.kind,
        active: stage.id === activeStageId,
        position: stage.position,
      })),
  };
}
