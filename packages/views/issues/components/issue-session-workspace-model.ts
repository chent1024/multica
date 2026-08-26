import type { AgentTask, IssueSessionStage, IssueSessionStageKind, TimelineEntry } from "@multica/core/types";

export const issueSessionStageKinds: readonly IssueSessionStageKind[] = [
  "analysis",
  "implementation",
  "review",
  "test",
  "commit",
  "push",
  "release",
];

export type IssueSessionProgressGroupKind =
  | "analysis"
  | "implementation"
  | "validation"
  | "delivery";

const progressGroupStages: ReadonlyArray<{
  kind: IssueSessionProgressGroupKind;
  stages: readonly IssueSessionStageKind[];
}> = [
  { kind: "analysis", stages: ["analysis"] },
  { kind: "implementation", stages: ["implementation"] },
  { kind: "validation", stages: ["review", "test"] },
  { kind: "delivery", stages: ["commit", "push", "release"] },
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
  latestOutcome: string | null;
  occurrences: Array<{
    id: string;
    kind: IssueSessionStageKind;
    active: boolean;
    position: number;
  }>;
  progressGroups: Array<{
    kind: IssueSessionProgressGroupKind;
    status: "completed" | "active" | "upcoming";
  }>;
}

export function buildIssueSessionWorkspaceModel(
  stages: IssueSessionStage[],
  activeStageId: string,
  continuityState: "fresh" | "pending" | "resumed" | "gap",
  tasks: AgentTask[],
  timeline: TimelineEntry[],
): IssueSessionWorkspaceModel | null {
  const active = stages.find((stage) => stage.id === activeStageId);
  if (!active) return null;

  const stageTasks = tasks
    .filter((task) => task.stage_id === activeStageId)
    .toSorted((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at));
  const latestStageTask = stageTasks[0] ?? null;
  const activeTask = tasks
    .filter((task) => task.session_id === active.session_id)
    .toSorted((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at))
    .find((task) =>
    ["queued", "dispatched", "waiting_local_directory", "waiting_previous_run", "running"].includes(task.status),
  ) ?? null;
  const taskOutcome = latestStageTask
    ? timeline
        .filter((entry) => entry.source_task_id === latestStageTask.id && entry.type === "comment" && entry.actor_type === "agent" && entry.content?.trim())
        .toSorted((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at))[0]
        ?.content?.trim() ?? resultSummary(latestStageTask.result) ?? latestStageTask.error?.trim() ?? null
    : null;

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
    latestOutcome: taskOutcome,
    occurrences: stages
      .toSorted((left, right) => left.position - right.position)
      .map((stage) => ({
        id: stage.id,
        kind: stage.kind,
        active: stage.id === activeStageId,
        position: stage.position,
      })),
    progressGroups: progressGroupStages.map((group, index) => {
      const activeGroupIndex = progressGroupStages.findIndex((candidate) =>
        candidate.stages.includes(active.kind),
      );
      return {
        kind: group.kind,
        status: index === activeGroupIndex
          ? "active"
          : index < activeGroupIndex && stages.some(
                (stage) =>
                  stage.status === "completed" && group.stages.includes(stage.kind),
              )
            ? "completed"
            : "upcoming",
      };
    }),
  };
}

function resultSummary(result: unknown): string | null {
  if (!result || typeof result !== "object") return null;
  const summary = (result as { summary?: unknown }).summary;
  return typeof summary === "string" && summary.trim() ? summary.trim() : null;
}
