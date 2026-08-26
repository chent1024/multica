import type { AgentTask, IssueSessionStage, IssueSessionStageKind } from "@multica/core/types";

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
  latestStageTask: AgentTask | null;
  activeTask: AgentTask | null;
  stageRunCount: number;
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
  tasks: AgentTask[],
): IssueSessionWorkspaceModel | null {
  const active = stages.find((stage) => stage.id === activeStageId);
  if (!active) return null;

  const stageTasks = tasks
    .filter((task) => task.stage_id === activeStageId)
    .toSorted((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at));
  const latestStageTask = stageTasks[0] ?? null;
  const activeTask = stageTasks.find((task) =>
    ["queued", "dispatched", "waiting_local_directory", "waiting_previous_run", "running"].includes(task.status),
  ) ?? null;
  return {
    active,
    recommendedNext: recommendedNextStage[active.kind],
    availableStages: issueSessionStageKinds.filter(
      (kind) => kind !== active.kind,
    ),
    hasContinuityGap: continuityState === "gap",
    latestStageTask,
    activeTask,
    stageRunCount: stageTasks.length,
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
