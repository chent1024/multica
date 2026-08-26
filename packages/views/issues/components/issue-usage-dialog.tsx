"use client";

import { useMemo } from "react";
import type { AgentTask } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import {
  formatTokens,
  inputCacheHitPercent,
  summarizeTaskUsage,
  summarizeTaskUsageAcross,
  type TaskUsageSummary,
} from "../../runtimes/utils";
import { KpiCard } from "../../runtimes/components/shared";
import { taskDurationMs, useStatusLabel, useTriggerText } from "./task-run-labels";
import { TaskStatusIcon } from "./task-status-icon";

// Token-focused breakdown for one local Issue. The task-runs query owns both
// the per-run rows and this aggregate so the header, dialog, and execution log
// cannot disagree while independent requests settle.
export function IssueUsageDialog({
  open,
  onOpenChange,
  identifier,
  tasks,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  identifier: string;
  tasks: AgentTask[];
}) {
  const { t } = useT("issues");
  const recorded = useMemo(
    () => tasks.filter((task) => (task.usage?.length ?? 0) > 0),
    [tasks],
  );
  const unknownCount = tasks.length - recorded.length;
  const total = useMemo(
    () => summarizeTaskUsageAcross(recorded.map((task) => task.usage)),
    [recorded],
  );
  const cacheHitRate = total
    ? inputCacheHitPercent(total.input, total.cacheRead)
    : null;
  const cumulativeDuration = formatCumulativeDuration(
    tasks.reduce((sum, task) => sum + (taskDurationMs(task) ?? 0), 0),
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="!max-w-6xl !w-[calc(100vw-4rem)]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.usage_detail.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.usage_detail.subtitle, {
              identifier,
              count: recorded.length,
            })}
          </DialogDescription>
        </DialogHeader>

        {total == null ? (
          <p className="py-8 text-center text-body text-muted-foreground">
            {t(($) => $.usage_detail.empty)}
          </p>
        ) : (
          <div className="flex min-w-0 flex-col gap-5">
            <div className="grid grid-cols-2 divide-x divide-y rounded-lg border bg-card lg:grid-cols-4 lg:divide-y-0">
              <KpiCard
                label={t(($) => $.usage_detail.kpi_total)}
                value={formatTokens(total.tokens)}
                hint={t(($) => $.usage_detail.kpi_total_hint, {
                  input: formatTokens(total.input),
                  cacheWrite: formatTokens(total.cacheWrite),
                })}
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_cache_hit)}
                value={cacheHitRate == null ? "—" : `${cacheHitRate}%`}
                accent={cacheHitRate != null && cacheHitRate > 0 ? "success" : "default"}
                hint={
                  cacheHitRate == null
                    ? t(($) => $.usage_detail.kpi_cache_unavailable)
                    : t(($) => $.usage_detail.kpi_cache_hint, {
                        input: formatTokens(total.input),
                        reads: formatTokens(total.cacheRead),
                      })
                }
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_output)}
                value={formatTokens(total.output)}
                hint={t(($) => $.usage_detail.kpi_output_hint)}
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_runs)}
                value={String(tasks.length)}
                hint={t(($) => $.usage_detail.kpi_runs_hint, {
                  duration: cumulativeDuration,
                })}
              />
            </div>

            <RunTable tasks={recorded} total={total} />

            {unknownCount > 0 && (
              <p className="text-micro text-muted-foreground">
                {t(($) => $.usage_detail.note_unknown, { count: unknownCount })}
              </p>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function RunTable({ tasks, total }: { tasks: AgentTask[]; total: TaskUsageSummary }) {
  const { t } = useT("issues");
  const maxTokens = tasks.reduce(
    (max, task) => Math.max(max, summarizeTaskUsage(task.usage)?.tokens ?? 0),
    0,
  );
  const totalHitRate = inputCacheHitPercent(total.input, total.cacheRead);
  const totalDuration = formatCumulativeDuration(
    tasks.reduce((sum, task) => sum + (taskDurationMs(task) ?? 0), 0),
  );

  return (
    <div className="max-h-[48vh] min-w-0 overflow-auto">
      <table className="w-full min-w-[68rem]">
        <thead className="sticky top-0 bg-popover">
          <tr className="text-micro text-muted-foreground [&>th]:whitespace-nowrap [&>th]:px-2 [&>th]:pb-1.5 [&>th]:text-right [&>th]:font-normal">
            <th className="!pl-0 !text-left">{t(($) => $.usage_detail.col_run)}</th>
            <th className="!text-left">{t(($) => $.usage_detail.col_status)}</th>
            <th className="!text-left">{t(($) => $.usage_detail.col_stage)}</th>
            <th>{t(($) => $.usage_detail.col_model)}</th>
            <th>{t(($) => $.usage_detail.col_input)}</th>
            <th>{t(($) => $.usage_detail.col_cache_read)}</th>
            <th>{t(($) => $.usage_detail.col_cache_write)}</th>
            <th>{t(($) => $.usage_detail.col_hit_rate)}</th>
            <th>{t(($) => $.usage_detail.col_output)}</th>
            <th className="!pr-10">{t(($) => $.usage_detail.col_tokens)}</th>
            <th className="!pr-0">{t(($) => $.usage_detail.col_duration)}</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <RunRow key={task.id} task={task} maxTokens={maxTokens} />
          ))}
        </tbody>
        <tfoot>
          <tr className="text-caption font-medium tabular-nums [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:border-input [&>td]:px-2 [&>td]:py-2 [&>td]:text-right">
            <td className="!pl-0 !text-left">{t(($) => $.usage_detail.total)}</td>
            <td colSpan={3} />
            <td>{formatTokens(total.input)}</td>
            <td>{formatTokens(total.cacheRead)}</td>
            <td>{formatTokens(total.cacheWrite)}</td>
            <td>{totalHitRate == null ? "—" : `${totalHitRate}%`}</td>
            <td>{formatTokens(total.output)}</td>
            <td className="!pr-10">{formatTokens(total.tokens)}</td>
            <td className="!pr-0">{totalDuration}</td>
          </tr>
        </tfoot>
      </table>
    </div>
  );
}

function RunRow({ task, maxTokens }: { task: AgentTask; maxTokens: number }) {
  const { t } = useT("issues");
  const trigger = useTriggerText(task);
  const statusLabel = useStatusLabel(task.status);
  const summary = summarizeTaskUsage(task.usage);
  if (!summary) return null;

  const duration =
    task.started_at && task.completed_at
      ? formatDuration(task.started_at, new Date(task.completed_at).getTime())
      : "";
  const hitRate = inputCacheHitPercent(summary.input, summary.cacheRead);
  const stage = task.stage_kind
    ? t(($) => $.issue_session.stages[task.stage_kind!])
    : "—";

  return (
    <tr className="text-caption tabular-nums transition-colors hover:bg-accent/40 [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:px-2 [&>td]:py-1.5 [&>td]:text-right">
      <td className="!pl-0 !text-left">
        <div className="flex items-center gap-2">
          <ActorAvatar actorType="agent" actorId={task.agent_id} size="sm" enableHoverCard />
          <span className="max-w-[13rem] truncate">{trigger}</span>
        </div>
      </td>
      <td className="!text-left">
        <span className="inline-flex items-center gap-1.5">
          <TaskStatusIcon status={task.status} />
          <span>{statusLabel}</span>
        </span>
      </td>
      <td className="!text-left text-muted-foreground">{stage}</td>
      <td className="max-w-[11rem] text-micro text-muted-foreground">
        <span className="block truncate" title={summary.models.join(", ")}>
          {summary.models.join(", ") || "—"}
        </span>
      </td>
      <td>{formatTokens(summary.input)}</td>
      <td>{formatTokens(summary.cacheRead)}</td>
      <td>{formatTokens(summary.cacheWrite)}</td>
      <td>{hitRate == null ? "—" : `${hitRate}%`}</td>
      <td>{formatTokens(summary.output)}</td>
      <td className="!pr-2">
        <div className="flex items-center justify-end gap-2">
          <span>{formatTokens(summary.tokens)}</span>
          <span className="relative h-1 w-8 overflow-hidden rounded-full bg-muted">
            <span
              className="absolute inset-y-0 left-0 rounded-full bg-chart-1"
              style={{ width: `${maxTokens > 0 ? (summary.tokens / maxTokens) * 100 : 0}%` }}
            />
          </span>
        </div>
      </td>
      <td className="!pr-0 text-muted-foreground">{duration || "—"}</td>
    </tr>
  );
}

function formatCumulativeDuration(ms: number): string {
  if (ms <= 0) return "—";
  const seconds = Math.max(1, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${String(minutes % 60).padStart(2, "0")}m`;
}
