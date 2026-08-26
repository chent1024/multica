// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentTask, TaskUsage } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

import { IssueUsageDialog } from "./issue-usage-dialog";

function makeTask(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "completed",
    priority: 0,
    dispatched_at: null,
    started_at: "2026-08-05T08:00:00Z",
    completed_at: "2026-08-05T08:11:00Z",
    result: null,
    error: null,
    created_at: "2026-08-05T08:00:00Z",
    trigger_summary: "Initial run",
    stage_kind: "implementation",
    ...overrides,
  };
}

function usage(overrides: Partial<TaskUsage> = {}): TaskUsage {
  return {
    provider: "openai",
    model: "gpt-5.6-sol",
    input_tokens: 1_000,
    output_tokens: 1_000,
    cache_read_tokens: 1_000,
    cache_write_tokens: 0,
    ...overrides,
  };
}

function open(tasks: AgentTask[]) {
  return renderWithI18n(
    <IssueUsageDialog open onOpenChange={() => {}} identifier="MUL-1" tasks={tasks} />,
  );
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
});

afterEach(cleanup);

describe("IssueUsageDialog", () => {
  it("calculates one weighted cache hit rate from aggregated counters", () => {
    open([
      makeTask({
        id: "tiny-hit",
        usage: [usage({ input_tokens: 0, cache_read_tokens: 1 })],
      }),
      makeTask({
        id: "large-miss",
        usage: [usage({ input_tokens: 999, cache_read_tokens: 0 })],
      }),
    ]);

    expect(screen.getAllByText("0%").length).toBeGreaterThan(0);
    expect(screen.queryByText("50%")).not.toBeInTheDocument();
  });

  it("floors a near-perfect rate and only shows 100% for a real full hit", () => {
    const { unmount } = open([
      makeTask({
        usage: [usage({ input_tokens: 573_500, cache_read_tokens: 126_000_000 })],
      }),
    ]);
    expect(screen.getAllByText("99%").length).toBeGreaterThan(0);
    expect(screen.queryByText("100%")).not.toBeInTheDocument();

    unmount();
    open([makeTask({ usage: [usage({ input_tokens: 0, cache_read_tokens: 1_000 })] })]);
    expect(screen.getAllByText("100%").length).toBeGreaterThan(0);
  });

  it("shows an unavailable cache rate when no input was measured", () => {
    open([
      makeTask({
        usage: [usage({ input_tokens: 0, cache_read_tokens: 0, output_tokens: 20 })],
      }),
    ]);

    expect(screen.getByText("No input tokens to measure")).toBeInTheDocument();
  });

  it("shows local token KPIs, stage, status, duration, and no currency analysis", () => {
    open([
      makeTask({
        usage: [usage({ cost_usd_ticks: 50_000_000_000 })],
      }),
    ]);

    expect(screen.getByText("Processed tokens")).toBeInTheDocument();
    expect(screen.getByText("Input cache hit rate")).toBeInTheDocument();
    expect(screen.getByText("Output tokens")).toBeInTheDocument();
    expect(screen.getByText("Runs")).toBeInTheDocument();
    expect(screen.getByText("Build")).toBeInTheDocument();
    expect(screen.getByText("Completed")).toBeInTheDocument();
    expect(screen.getAllByText("11m 00s").length).toBeGreaterThan(0);
    expect(screen.queryByText("Cost")).not.toBeInTheDocument();
    expect(screen.queryByText(/\$/)).not.toBeInTheDocument();
  });

  it("keeps historical unknown usage distinct from zero", () => {
    open([
      makeTask({ id: "recorded", usage: [usage()] }),
      makeTask({ id: "unknown", usage: [] }),
    ]);

    expect(
      screen.getByText("1 run has no recorded usage and is not counted in token totals."),
    ).toBeInTheDocument();
  });

  it("keeps the full model list reachable when the cell truncates", () => {
    const models = "gpt-5.6-luna, gpt-5.6-sol[1m]";
    open([
      makeTask({
        usage: [
          usage({ model: "gpt-5.6-luna" }),
          usage({ model: "gpt-5.6-sol[1m]" }),
        ],
      }),
    ]);

    expect(screen.getByTitle(models)).toBeInTheDocument();
  });

  it("lets the run table scroll rather than widening the dialog", () => {
    open([makeTask({ usage: [usage()] })]);

    const table = screen.getByRole("table");
    const scroller = table.parentElement;
    expect(scroller?.className).toContain("overflow-auto");
    expect(scroller?.className).toContain("min-w-0");
  });
});
