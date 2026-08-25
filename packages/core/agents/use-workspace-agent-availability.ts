"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "../hooks";
import { agentListOptions } from "../workspace/queries";
import { isAgentRuntimeBound } from "./runtime-binding";

/**
 * Three-state availability for "does the current user have any agent
 * they can chat with in this workspace?".
 *
 * Why three states (not a boolean): the answer to "is there an agent?"
 * lives on the server. Until the agent-list query resolves, the answer
 * is genuinely *unknown*. Callers must distinguish "loading" from
 * "confirmed empty" — collapsing them to a boolean causes UIs to flash
 * disabled/empty states for the first few hundred ms after mount, even
 * when the workspace actually has agents.
 *
 *   "loading"   — agent list still in flight (be neutral in UI)
 *   "none"      — the query resolved with zero runnable local agents
 *   "available" — at least one non-archived agent has a bound runtime
 */
export type WorkspaceAgentAvailability = "loading" | "none" | "available";

/**
 * Local mode has no authenticated user, member role, or private-agent
 * permission gate. Reachability is purely local runtime state.
 */
export function useWorkspaceAgentAvailability(): WorkspaceAgentAvailability {
  const wsId = useWorkspaceId();
  const { data: agents, isFetched: agentsFetched } = useQuery(
    agentListOptions(wsId),
  );
  if (!agentsFetched) return "loading";

  const hasVisibleAgent = (agents ?? []).some(
    (a) => !a.archived_at && isAgentRuntimeBound(a),
  );

  return hasVisibleAgent ? "available" : "none";
}
