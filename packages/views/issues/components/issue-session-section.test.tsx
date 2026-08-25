// @vitest-environment jsdom
import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { IssueSession } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const state = vi.hoisted(() => ({
  mutate: vi.fn(),
  pending: false,
  error: false,
  loadError: false,
  session: {
    id: "session-1", issue_id: "child-1", workspace_id: "ws-1", active_stage_id: "stage-2",
    continuity_state: "gap", continuity_reason: "provider_thread_replaced", created_at: "2026-01-01", updated_at: "2026-01-01",
    stages: [
      { id: "stage-1", session_id: "session-1", kind: "analysis", status: "completed", position: 0, migrated: false, started_at: "2026-01-01", completed_at: "2026-01-01" },
      { id: "stage-2", session_id: "session-1", kind: "review", status: "active", position: 1, migrated: false, started_at: "2026-01-01", completed_at: null },
    ],
    handoff: {
      id: "handoff-1", child_issue_id: "child-1", parent_issue_id: "parent-1", parent_session_id: "parent-session",
      source_stage_id: "parent-stage", source_run_id: null, objective: "Verify the child flow", confirmed_facts: ["Contract is fixed"],
      decisions: [], constraints: ["No cloud scope"], acceptance_criteria: [], references: [], open_questions: [], created_at: "2026-01-01",
    },
  } as IssueSession,
}));

vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ issueDetail: (id: string) => `/issues/${id}` }) }));
vi.mock("../../navigation", () => ({ AppLink: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a href={href} {...props}>{children}</a> }));
vi.mock("@multica/core/issues/session", () => ({
  issueSessionOptions: () => ({
    queryKey: ["issues", "session", "child-1"],
    queryFn: async () => {
      if (state.loadError) throw new Error("unavailable");
      return state.session;
    },
  }),
  useActivateIssueSessionStage: () => ({ mutate: state.mutate, isPending: state.pending, isError: state.error }),
}));

import { IssueSessionWorkspace } from "./issue-session-section";

function renderWorkspace() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(<QueryClientProvider client={client}><IssueSessionWorkspace issueId="child-1" /></QueryClientProvider>);
}

describe("IssueSessionWorkspace", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    state.pending = false;
    state.error = false;
    state.loadError = false;
    state.session.active_stage_id = "stage-2";
    state.session.stages = [
      { id: "stage-1", session_id: "session-1", kind: "analysis", status: "completed", position: 0, migrated: false, started_at: "2026-01-01", completed_at: "2026-01-01" },
      { id: "stage-2", session_id: "session-1", kind: "review", status: "active", position: 1, migrated: false, started_at: "2026-01-01", completed_at: null },
    ];
  });

  it("leads with the current goal, evidence, recovery, and inherited context", async () => {
    renderWorkspace();
    expect(await screen.findByText("Check scope and correctness")).toBeInTheDocument();
    expect(screen.getByText("Review result and concrete findings")).toBeInTheDocument();
    expect(screen.getByText("Execution context was not fully resumed")).toBeInTheDocument();
    expect(screen.getByText(/review the latest outcome/i)).toBeInTheDocument();
    expect(screen.getByText("Verify the child flow")).toBeInTheDocument();
    expect(screen.getByText("Contract is fixed")).toBeInTheDocument();
    expect(screen.getByText("No cloud scope")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open parent" })).toHaveAttribute("href", "/issues/parent-1");
    expect(screen.queryByText("Decisions")).not.toBeInTheDocument();
  });

  it("offers one recommended progression and records no delivery side effect", async () => {
    renderWorkspace();
    fireEvent.click(await screen.findByRole("button", { name: /continue to test/i }));
    expect(state.mutate).toHaveBeenCalledWith("test");
    expect(screen.getByText(/records progress only/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /commit now|push now|release now/i })).not.toBeInTheDocument();
  });

  it("keeps non-linear stage choices and repeated history available", async () => {
    renderWorkspace();
    await screen.findByText("Check scope and correctness");
    fireEvent.click(screen.getByRole("button", { name: "Change stage" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Build" }));
    expect(state.mutate).toHaveBeenCalledWith("implementation");
    fireEvent.click(screen.getByText("Journey details"));
    expect(screen.getByText("Analysis → Review")).toBeInTheDocument();
  });

  it("shows an accessible inline mutation error", async () => {
    state.error = true;
    renderWorkspace();
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not change stage.");
  });

  it("keeps the issue usable with a retryable session load error", async () => {
    state.loadError = true;
    renderWorkspace();

    const error = await screen.findByRole("alert");
    expect(error).toHaveTextContent("Could not load issue session.");
    expect(screen.getByRole("button", { name: "Try again" })).toBeEnabled();
  });

  it("does not invent a successor after release", async () => {
    state.session.active_stage_id = "stage-release";
    state.session.stages = [
      { id: "stage-release", session_id: "session-1", kind: "release", status: "active", position: 0, migrated: false, started_at: "2026-01-01", completed_at: null },
    ];
    renderWorkspace();

    expect(await screen.findByText("Release an explicitly authorized version")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /continue to/i })).not.toBeInTheDocument();
    expect(screen.getByText(/follow-up work/i)).toBeInTheDocument();
  });
});
