import type { AgentTask, IssueSession, IssueSessionStageKind } from "@multica/core/types";

export interface IssueConversationMarker {
  id: string;
  type: "stage" | "run";
  createdAt: string;
  stageKind: IssueSessionStageKind;
  stagePosition: number;
  status?: AgentTask["status"];
  task?: AgentTask;
}

export function buildIssueConversationMarkers(
  session: IssueSession | undefined,
  tasks: readonly AgentTask[],
): IssueConversationMarker[] {
  if (!session) return [];
  const positionByStage = new Map(session.stages.map((stage) => [stage.id, stage.position]));
  const kindByStage = new Map(session.stages.map((stage) => [stage.id, stage.kind]));
  return [
    ...session.stages.map<IssueConversationMarker>((stage) => ({
      id: `stage:${stage.id}`,
      type: "stage",
      createdAt: stage.started_at,
      stageKind: stage.kind,
      stagePosition: stage.position,
    })),
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
    if (left.type !== right.type) return left.type === "stage" ? -1 : 1;
    return left.id.localeCompare(right.id);
  });
}
