import type { AgentTask, IssueSession, IssueSessionStageKind, TimelineEntry } from "@multica/core/types";

export interface IssueConversationMarker {
  id: string;
  type: "stage" | "run";
  createdAt: string;
  stageKind: IssueSessionStageKind;
  stagePosition: number;
  stageStatus?: "active" | "completed";
  runCount?: number;
  latestOutcome?: string;
  status?: AgentTask["status"];
  task?: AgentTask;
}

export function buildIssueConversationMarkers(
  session: IssueSession | undefined,
  tasks: readonly AgentTask[],
  timeline: readonly TimelineEntry[] = [],
): IssueConversationMarker[] {
  if (!session) return [];
  const positionByStage = new Map(session.stages.map((stage) => [stage.id, stage.position]));
  const kindByStage = new Map(session.stages.map((stage) => [stage.id, stage.kind]));
  return [
    ...session.stages.map<IssueConversationMarker>((stage) => {
      const stageTasks = tasks
        .filter((task) => task.stage_id === stage.id)
        .toSorted((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at));
      const latestTask = stageTasks[0];
      const timelineOutcome = latestTask
        ? timeline
            .filter(
              (entry) =>
                entry.source_task_id === latestTask.id &&
                entry.type === "comment" &&
                entry.actor_type === "agent" &&
                entry.content?.trim(),
            )
            .toSorted(
              (left, right) => Date.parse(right.created_at) - Date.parse(left.created_at),
            )[0]
            ?.content?.trim()
        : undefined;
      const latestOutcome = latestTask
        ? timelineOutcome ?? resultSummary(latestTask.result) ?? latestTask.error?.trim() ?? undefined
        : undefined;
      return {
        id: `stage:${stage.id}`,
        type: "stage",
        createdAt: stage.started_at,
        stageKind: stage.kind,
        stagePosition: stage.position,
        stageStatus: stage.status,
        runCount: stageTasks.length,
        latestOutcome,
      };
    }),
    ...tasks.flatMap<IssueConversationMarker>((task) => {
      const stageKind = task.stage_kind ?? (task.stage_id ? kindByStage.get(task.stage_id) : undefined);
      if (!stageKind) return [];
      return [{
        id: `run:${task.id}`,
        type: "run",
        createdAt: task.created_at,
        stageKind,
        stagePosition: task.stage_id ? (positionByStage.get(task.stage_id) ?? Number.MAX_SAFE_INTEGER) : Number.MAX_SAFE_INTEGER,
        status: task.status,
        task,
      }];
    }),
  ].toSorted((left, right) => {
    const time = Date.parse(left.createdAt) - Date.parse(right.createdAt);
    if (time !== 0) return time;
    if (left.stagePosition !== right.stagePosition) {
      return left.stagePosition - right.stagePosition;
    }
    if (left.type !== right.type) return left.type === "stage" ? -1 : 1;
    return left.id.localeCompare(right.id);
  });
}

function resultSummary(result: unknown): string | undefined {
  if (!result || typeof result !== "object") return undefined;
  const summary = (result as { summary?: unknown }).summary;
  return typeof summary === "string" && summary.trim() ? summary.trim() : undefined;
}
