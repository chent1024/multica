import type { AgentTask, IssueSession, IssueSessionStageKind } from "@multica/core/types";

export interface IssueConversationMarker {
  id: string;
  type: "stage";
  createdAt: string;
  stageKind: IssueSessionStageKind;
  stagePosition: number;
  stageStatus?: "active" | "completed";
  runCount?: number;
}

export function buildIssueConversationMarkers(
  session: IssueSession | undefined,
  tasks: readonly AgentTask[],
): IssueConversationMarker[] {
  if (!session) return [];
  // The current-work strip already names the first/current stage. Conversation
  // markers exist only to show a real transition, so the initial stage must not
  // be repeated immediately below that strip.
  return session.stages
    .slice(1)
    .map<IssueConversationMarker>((stage) => {
      const stageTasks = tasks
        .filter((task) => task.stage_id === stage.id)
        .length;
      return {
        id: `stage:${stage.id}`,
        type: "stage",
        createdAt: stage.started_at,
        stageKind: stage.kind,
        stagePosition: stage.position,
        stageStatus: stage.status,
        runCount: stageTasks,
      };
    })
    .toSorted((left, right) => {
      const time = Date.parse(left.createdAt) - Date.parse(right.createdAt);
      if (time !== 0) return time;
      if (left.stagePosition !== right.stagePosition) {
        return left.stagePosition - right.stagePosition;
      }
      return left.id.localeCompare(right.id);
    });
}
