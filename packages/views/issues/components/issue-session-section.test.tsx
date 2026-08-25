// @vitest-environment jsdom
import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const state = vi.hoisted(() => ({ mutate: vi.fn(), pending: false, error: false }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ issueDetail: (id: string) => `/issues/${id}` }) }));
vi.mock("../../navigation", () => ({ AppLink: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a href={href} {...props}>{children}</a> }));
vi.mock("@multica/core/issues/session", () => ({
  issueSessionOptions: () => ({
    queryKey: ["issues", "session", "child-1"],
    queryFn: async () => ({
      id: "session-1", issue_id: "child-1", workspace_id: "ws-1", active_stage_id: "stage-2",
      continuity_state: "gap", continuity_reason: "provider_thread_replaced", created_at: "2026-01-01", updated_at: "2026-01-01",
      stages: [
        { id: "stage-1", session_id: "session-1", kind: "analysis", status: "completed", position: 0, migrated: false, started_at: "2026-01-01", completed_at: "2026-01-01" },
        { id: "stage-2", session_id: "session-1", kind: "review", status: "active", position: 1, migrated: false, started_at: "2026-01-01", completed_at: null },
      ],
      handoff: {
        id: "handoff-1", child_issue_id: "child-1", parent_issue_id: "parent-1", parent_session_id: "parent-session",
        source_stage_id: "parent-stage", objective: "Verify the child flow", confirmed_facts: ["Contract is fixed"],
        decisions: [], constraints: ["No cloud scope"], acceptance_criteria: [], references: [], open_questions: [], created_at: "2026-01-01",
      },
    }),
  }),
  useActivateIssueSessionStage: () => ({ mutate: state.mutate, isPending: state.pending, isError: state.error }),
}));

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { IssueSessionSection } from "./issue-session-section";

describe("IssueSessionSection", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows current continuity, inherited context, and parent provenance", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithI18n(<QueryClientProvider client={client}><IssueSessionSection issueId="child-1" /></QueryClientProvider>);
    expect(await screen.findByText("Context gap")).toBeInTheDocument();
		expect(screen.getByText("Analysis → Review")).toBeInTheDocument();
    expect(screen.getByText("Verify the child flow")).toBeInTheDocument();
    expect(screen.getByText("Contract is fixed")).toBeInTheDocument();
    expect(screen.getByText("No cloud scope")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open parent" })).toHaveAttribute("href", "/issues/parent-1");
  });

  it("requests a repeated stage without implying an external side effect", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithI18n(<QueryClientProvider client={client}><IssueSessionSection issueId="child-1" /></QueryClientProvider>);
    await screen.findByText("Context gap");
    fireEvent.click(screen.getByRole("button", { name: "Analysis" }));
    expect(state.mutate).toHaveBeenCalledWith("analysis");
    expect(screen.getByText(/does not commit, push, or release/i)).toBeInTheDocument();
  });
});
