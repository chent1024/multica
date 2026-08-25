/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { IssueSession } from "../types";
import { issueSessionKeys, issueSessionOptions, useActivateIssueSessionStage } from "./session";

const base: IssueSession = {
  id: "session-1", issue_id: "issue-1", workspace_id: "ws-1", active_stage_id: "stage-1",
  continuity_state: "fresh", continuity_reason: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  handoff: null,
  stages: [{ id: "stage-1", session_id: "session-1", kind: "analysis", status: "active", position: 0, migrated: false, started_at: "2026-01-01T00:00:00Z", completed_at: null }],
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
});
