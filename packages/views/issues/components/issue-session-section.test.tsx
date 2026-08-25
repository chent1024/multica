// @vitest-environment jsdom
import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AgentTask, Issue, IssueSession, TimelineEntry } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const state = vi.hoisted(() => ({
  mutate: vi.fn(),
  pending: false,
  error: false,
  loadError: false,
  continueMutateAsync: vi.fn(),
  continuePending: false,
  continueError: false,
  tasks: [] as AgentTask[],
  tasksError: false,
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
      source_stage_kind: "analysis", decisions: ["Use the existing session"], constraints: ["No cloud scope"], acceptance_criteria: ["Conversation opens"], references: [], open_questions: [], created_at: "2026-01-01",
    },
  } as IssueSession,
}));

const issue: Issue = {
  id: "child-1", workspace_id: "ws-1", number: 1, identifier: "MUL-1", title: "Child issue", description: null,
  status: "in_progress", priority: "none", assignee_type: "agent", assignee_id: "agent-1", creator_type: "member", creator_id: "member-1",
  parent_issue_id: "parent-1", project_id: null, position: 1, stage: null, start_date: null, due_date: null, metadata: {}, properties: {},
  created_at: "2026-01-01", updated_at: "2026-01-01",
};

const completedTask: AgentTask = {
  id: "run-completed", agent_id: "agent-1", runtime_id: "runtime-1", issue_id: "child-1", session_id: "session-1", stage_id: "stage-2", stage_kind: "review",
  status: "completed", priority: 0, dispatched_at: null, started_at: "2026-01-01", completed_at: "2026-01-01", result: { summary: "Review summary" },
  error: null, created_at: "2026-01-01",
};

const timeline: TimelineEntry[] = [{
  type: "comment", id: "outcome-1", actor_type: "agent", actor_id: "agent-1", source_task_id: "run-completed",
  content: "Review found no blocking issues.", created_at: "2026-01-01T02:00:00Z",
}];

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
  useContinueIssueSession: () => ({ mutateAsync: state.continueMutateAsync, isPending: state.continuePending, isError: state.continueError }),
}));
vi.mock("@multica/core/api", () => ({ api: { listTasksByIssue: vi.fn(async () => {
  if (state.tasksError) throw new Error("unavailable");
  return state.tasks;
}) } }));
vi.mock("../../common/task-transcript", () => ({
  TranscriptButton: ({ task, open }: { task: AgentTask; open?: boolean }) => open ? <div role="dialog">Transcript {task.id}</div> : null,
}));

import { IssueSessionWorkspace } from "./issue-session-section";

function renderWorkspace(issueOverride: Partial<Issue> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(<QueryClientProvider client={client}><IssueSessionWorkspace issue={{ ...issue, ...issueOverride }} timeline={timeline} /></QueryClientProvider>);
}

describe("IssueSessionWorkspace", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    state.pending = false;
    state.error = false;
    state.loadError = false;
    state.continuePending = false;
    state.continueError = false;
    state.tasks = [completedTask];
    state.tasksError = false;
    state.continueMutateAsync.mockResolvedValue({ outcome: "queued", issue, task: { ...completedTask, id: "run-new", status: "queued" } });
    state.session.active_stage_id = "stage-2";
    state.session.handoff = {
      id: "handoff-1", child_issue_id: "child-1", parent_issue_id: "parent-1", parent_session_id: "parent-session",
      source_stage_id: "parent-stage", source_run_id: null, objective: "Verify the child flow", confirmed_facts: ["Contract is fixed"],
      source_stage_kind: "analysis", decisions: ["Use the existing session"], constraints: ["No cloud scope"], acceptance_criteria: ["Conversation opens"], references: [], open_questions: [], created_at: "2026-01-01",
    };
    state.session.stages = [
      { id: "stage-1", session_id: "session-1", kind: "analysis", status: "completed", position: 0, migrated: false, started_at: "2026-01-01", completed_at: "2026-01-01" },
      { id: "stage-2", session_id: "session-1", kind: "review", status: "active", position: 1, migrated: false, started_at: "2026-01-01", completed_at: null },
    ];
  });

  it("leads with the current goal, evidence, recovery, and inherited context", async () => {
    renderWorkspace();
    expect(await screen.findByText("Check scope and correctness")).toBeInTheDocument();
    expect(screen.getByText("Review result and concrete findings")).toBeInTheDocument();
    expect(screen.getByText("Review found no blocking issues.")).toBeInTheDocument();
    expect(screen.getByText("Execution context was not fully resumed")).toBeInTheDocument();
    expect(screen.getByText(/review the latest outcome/i)).toBeInTheDocument();
    expect(screen.getByText("Verify the child flow")).toBeInTheDocument();
    expect(screen.getAllByText("Contract is fixed")).toHaveLength(2);
    expect(screen.getAllByText("No cloud scope")).toHaveLength(2);
    expect(screen.getByRole("link", { name: "Open parent" })).toHaveAttribute("href", "/issues/parent-1");
    expect(screen.getByText("Use the existing session")).not.toBeVisible();
    fireEvent.click(screen.getByText("Full handoff"));
    expect(screen.getByText("Use the existing session")).toBeInTheDocument();
    expect(screen.getByText("Conversation opens")).toBeInTheDocument();
  });

  it("builds the bounded handoff summary from every populated section", async () => {
    state.session.handoff = {
      ...state.session.handoff!,
      confirmed_facts: [],
      decisions: ["Keep the current contract"],
      constraints: [],
      acceptance_criteria: ["Conversation opens"],
      references: ["issue://parent-1"],
    };
    renderWorkspace();
    expect(await screen.findAllByText("Keep the current contract")).toHaveLength(2);
    expect(screen.getAllByText("Conversation opens")).toHaveLength(2);
    expect(screen.getByText("issue://parent-1")).not.toBeVisible();
  });

  it("continues the conversation as the primary action and opens the returned run", async () => {
    renderWorkspace();
    fireEvent.click(await screen.findByRole("button", { name: "Continue conversation" }));
    expect(state.continueMutateAsync).toHaveBeenCalledOnce();
    expect(await screen.findByRole("dialog")).toHaveTextContent("run-new");
  });

  it("opens the latest completed stage conversation without starting another run", async () => {
    renderWorkspace();
    fireEvent.click(await screen.findByRole("button", { name: "View latest conversation" }));
    expect(state.continueMutateAsync).not.toHaveBeenCalled();
    expect(await screen.findByRole("dialog")).toHaveTextContent("run-completed");
  });

  it("records the recommended stage separately from running work", async () => {
    renderWorkspace();
    fireEvent.click(await screen.findByRole("button", { name: /mark as test/i }));
    expect(state.mutate).toHaveBeenCalledWith("test");
    expect(screen.getByText(/records progress only/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /commit now|push now|release now/i })).not.toBeInTheDocument();
  });

  it("keeps non-linear stage choices and repeated history available", async () => {
    renderWorkspace();
    await screen.findByText("Check scope and correctness");
    fireEvent.click(screen.getByRole("button", { name: "Record another stage" }));
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

  it("reports a continuation failure without changing the recorded stage", async () => {
    state.continueError = true;
    renderWorkspace();
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not continue this conversation.");
    expect(state.mutate).not.toHaveBeenCalled();
  });

  it("does not offer a start action when conversation state cannot be loaded", async () => {
    state.tasksError = true;
    renderWorkspace();
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load conversation state.");
    expect(screen.getByRole("button", { name: "Start conversation" })).toBeDisabled();
  });

  it("opens an already active run without creating another one", async () => {
    state.tasks = [{ ...completedTask, id: "run-active", status: "running" }];
    renderWorkspace();
    fireEvent.click(await screen.findByRole("button", { name: "Open current conversation" }));
    expect(state.continueMutateAsync).not.toHaveBeenCalled();
    expect(await screen.findByRole("dialog")).toHaveTextContent("run-active");
  });

  it("does not offer execution for a closed issue", async () => {
    renderWorkspace({ status: "done" });
    expect(await screen.findByText("Reopen this issue before continuing its conversation.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start conversation|continue conversation|open current conversation/i })).not.toBeInTheDocument();
  });

  it("explains the bounded state change before starting parked work", async () => {
    state.tasks = [];
    renderWorkspace({ status: "backlog", assignee_type: null, assignee_id: null });
    expect(await screen.findByText("Starting the conversation assigns Codex and moves this issue into progress.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start conversation" })).toBeEnabled();
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
    expect(screen.queryByRole("button", { name: /mark as/i })).not.toBeInTheDocument();
    expect(screen.getByText(/follow-up work/i)).toBeInTheDocument();
  });
});
