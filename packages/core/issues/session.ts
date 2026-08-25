import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { IssueSession, IssueSessionStageKind } from "../types";

export const issueSessionKeys = {
  all: () => ["issues", "session"] as const,
  detail: (issueId: string) => [...issueSessionKeys.all(), issueId] as const,
};

export function issueSessionOptions(issueId: string) {
  return queryOptions({
    queryKey: issueSessionKeys.detail(issueId),
    queryFn: () => api.getIssueSession(issueId),
  });
}

export function useActivateIssueSessionStage(issueId: string) {
  const queryClient = useQueryClient();
  const queryKey = issueSessionKeys.detail(issueId);
  return useMutation({
    mutationFn: (kind: IssueSessionStageKind) => api.activateIssueSessionStage(issueId, kind),
    onMutate: async (kind) => {
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<IssueSession>(queryKey);
      if (previous) {
        const active = previous.stages.find((stage) => stage.id === previous.active_stage_id);
        if (active?.kind !== kind) {
          const stamp = new Date().toISOString();
          const optimisticId = `optimistic:${kind}`;
          queryClient.setQueryData<IssueSession>(queryKey, {
            ...previous,
            active_stage_id: optimisticId,
            updated_at: stamp,
            stages: [
              ...previous.stages.map((stage) =>
                stage.id === previous.active_stage_id
                  ? { ...stage, status: "completed" as const, completed_at: stamp }
                  : stage,
              ),
              {
                id: optimisticId,
                session_id: previous.id,
                kind,
                status: "active",
                position: previous.stages.length,
                migrated: false,
                started_at: stamp,
                completed_at: null,
              },
            ],
          });
        }
      }
      return { previous };
    },
    onError: (_error, _kind, context) => {
      if (context?.previous) queryClient.setQueryData(queryKey, context.previous);
    },
    onSuccess: (session) => queryClient.setQueryData(queryKey, session),
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  });
}
