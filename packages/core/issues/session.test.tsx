/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { AgentTask, Issue, IssueSession } from "../types";
import { issueKeys } from "./queries";
import { issueSessionKeys, issueSessionOptions, useActivateIssueSessionStage, useContinueIssueSession } from "./session";

const base: IssueSession = {
  id: "session-1", issue_id: "issue-1", workspace_id: "ws-1", active_stage_id: "stage-1",
  continuity_state: "fresh", continuity_reason: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  handoff: null,
  stages: [{ id: "stage-1", session_id: "session-1", kind: "analysis", status: "active", position: 0, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: null }],
};

const issue: Issue = {
  id: "issue-1", workspace_id: "ws-1", number: 1, identifier: "MUL-1", title: "Ship it", description: null,
  status: "in_progress", priority: "none", assignee_type: "agent", assignee_id: "agent-1", creator_type: "member",
  creator_id: "member-1", parent_issue_id: null, project_id: null, position: 1, stage: null, start_date: null,
  due_date: null, metadata: {}, properties: {}, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
};

const task: AgentTask = {
  id: "run-1", agent_id: "agent-1", runtime_id: "runtime-1", issue_id: "issue-1", status: "queued", priority: 0,
  dispatched_at: null, started_at: null, completed_at: null, result: null, error: null, created_at: "2026-01-01T00:00:00Z",
};

function wrapper(client: QueryClient) {
  return function QueryWrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

describe("issue session queries and stage mutation", () => {
  it("uses one stable per-issue key", () => {
    expect(issueSessionOptions("issue-1").queryKey).toEqual(issueSessionKeys.detail("issue-1"));
  });

  it("optimistically appends a stage and replaces it with the server result", async () => {
    let resolve!: (value: IssueSession) => void;
    const next = { ...base, active_stage_id: "stage-2", stages: [...base.stages.map((s) => ({ ...s, status: "completed" as const })), { ...base.stages[0]!, id: "stage-2", kind: "review" as const, status: "active" as const, position: 1 }] };
    setApiInstance({ activateIssueSessionStage: vi.fn(() => new Promise<IssueSession>((done) => { resolve = done; })) } as unknown as ApiClient);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    qc.setQueryData(issueSessionKeys.detail("issue-1"), base);
    const { result } = renderHook(() => useActivateIssueSessionStage("issue-1"), { wrapper: wrapper(qc) });
    act(() => result.current.mutate("review"));
    await waitFor(() => expect(qc.getQueryData<IssueSession>(issueSessionKeys.detail("issue-1"))?.stages).toHaveLength(2));
    resolve(next);
    await waitFor(() => expect(qc.getQueryData<IssueSession>(issueSessionKeys.detail("issue-1"))?.active_stage_id).toBe("stage-2"));
  });

  it("rolls back the exact prior session when activation fails", async () => {
    setApiInstance({ activateIssueSessionStage: vi.fn().mockRejectedValue(new Error("failed")) } as unknown as ApiClient);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    qc.setQueryData(issueSessionKeys.detail("issue-1"), base);
    const { result } = renderHook(() => useActivateIssueSessionStage("issue-1"), { wrapper: wrapper(qc) });
    act(() => result.current.mutate("release"));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(qc.getQueryData(issueSessionKeys.detail("issue-1"))).toEqual(base);
  });

  it("adopts continuation results in issue and execution-log caches without duplicates", async () => {
    setApiInstance({
      continueIssueSession: vi.fn().mockResolvedValue({ outcome: "queued", issue, task }),
    } as unknown as ApiClient);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    qc.setQueryData(issueKeys.tasks("issue-1"), [task]);
    const { result } = renderHook(() => useContinueIssueSession("ws-1", "issue-1"), { wrapper: wrapper(qc) });

    await act(async () => { await result.current.mutateAsync(); });

    expect(qc.getQueryData(issueKeys.detail("ws-1", "issue-1"))).toEqual(issue);
    expect(qc.getQueryData<AgentTask[]>(issueKeys.tasks("issue-1"))).toEqual([task]);
  });
});
