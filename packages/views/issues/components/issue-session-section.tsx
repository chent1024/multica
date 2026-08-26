"use client";

import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowRight, CircleCheck, CircleDot, MoreHorizontal } from "lucide-react";
import type { Issue, IssueSessionStageKind } from "@multica/core/types";
import { issueStatusCategory } from "@multica/core/issues";
import { issueKeys } from "@multica/core/issues/queries";
import { api } from "@multica/core/api";
import { issueSessionOptions, useActivateIssueSessionStage } from "@multica/core/issues/session";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useWorkspacePaths } from "@multica/core/paths";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { useStatusLabel } from "../utils/status-label";
import { buildIssueSessionWorkspaceModel } from "./issue-session-workspace-model";

interface IssueSessionWorkspaceProps {
  issue: Issue;
  onContinueConversation?: () => void;
}

export function IssueSessionWorkspace({ issue, onContinueConversation }: IssueSessionWorkspaceProps) {
  const issueId = issue.id;
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const statusLabel = useStatusLabel(issue.workspace_id);
  const { data: session, isLoading, isError, refetch } = useQuery({
    ...issueSessionOptions(issueId),
  });
  const { data: tasks = [], isError: tasksError } = useQuery({
    queryKey: issueKeys.tasks(issueId),
    queryFn: () => api.listTasksByIssue(issueId),
  });
  const activate = useActivateIssueSessionStage(issueId);

  if (isLoading) {
    return (
      <section data-issue-session-workspace className="mt-3 border-y border-border/70 py-3 text-body text-muted-foreground">
        {t(($) => $.issue_session.loading)}
      </section>
    );
  }

  const model = session
    ? buildIssueSessionWorkspaceModel(
        session.stages,
        session.active_stage_id,
        session.continuity_state,
        tasks,
      )
    : null;

  if (isError || !session || !model) {
    return (
      <section data-issue-session-workspace role="alert" className="mt-3 border-y border-destructive/30 bg-destructive/5 py-3 text-body text-destructive">
        <span>{t(($) => $.issue_session.load_failed)}</span>
        <Button type="button" size="sm" variant="outline" className="ml-3" onClick={() => void refetch()}>
          {t(($) => $.issue_session.retry)}
        </Button>
      </section>
    );
  }

  const activateStage = (kind: IssueSessionStageKind) => activate.mutate(kind);
  const statusCategory = issueStatusCategory(issue);
  const closed = statusCategory === "done" || statusCategory === "cancelled";
  const parked = statusCategory === "backlog";
  const blocked = statusCategory === "blocked";
  const stageTask = model.activeTask ?? model.latestStageTask;
  const stageTaskStatus = stageTask?.status;
  const stageFinished = !model.activeTask && stageTaskStatus === "completed";
  const stageLabel = t(($) => $.issue_session.stages[model.active.kind]);
  const runLabel = stageTaskStatus
    ? t(($) => $.issue_session.run_status[stageTaskStatus])
    : t(($) => $.issue_session.no_runs);
  const compactHandoff = session.handoff
    ? [
        ...session.handoff.confirmed_facts,
        ...session.handoff.constraints,
        ...session.handoff.decisions,
        ...session.handoff.acceptance_criteria,
        ...session.handoff.references,
        ...session.handoff.open_questions,
      ].filter(Boolean).slice(0, 2)
    : [];

  return (
    <section data-issue-session-workspace aria-labelledby="issue-current-work-title" className="mt-3 border-y border-border/70">
      <div className="flex flex-col gap-3 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-caption">
            <span className="inline-flex items-center gap-1.5">
              <span className="text-muted-foreground">{t(($) => $.issue_session.task_status_label)}</span>
              <span id="issue-current-work-title" className="font-semibold text-foreground">{statusLabel(issue.status)}</span>
            </span>
            <span aria-hidden="true" className="text-muted-foreground/40">·</span>
            <span className="inline-flex items-center gap-1.5">
              <span className="text-muted-foreground">{t(($) => $.issue_session.work_stage_label)}</span>
              {stageFinished ? <CircleCheck className="size-3.5 text-success" aria-hidden="true" /> : <CircleDot className="size-3.5 text-primary" aria-hidden="true" />}
              <span className="font-medium text-foreground">{stageLabel}</span>
              <span className="text-muted-foreground">{runLabel}</span>
              {model.stageRunCount > 0 && <span className="text-muted-foreground">· {t(($) => $.issue_session.stage_run_count, { count: model.stageRunCount })}</span>}
            </span>
          </div>
          <p className="mt-1 text-caption text-muted-foreground">
            {blocked ? t(($) => $.issue_session.blocked_next_step) : t(($) => $.issue_session.stage_guidance[model.active.kind].goal)}
          </p>
        </div>

        {!closed && (
          <div className="flex shrink-0 flex-wrap items-center gap-1.5">
            {(blocked || parked) && onContinueConversation ? (
              <Button type="button" size="sm" onClick={onContinueConversation}>{t(($) => $.issue_session.continue_conversation)}</Button>
            ) : model.recommendedNext && stageFinished ? (
              <Button type="button" size="sm" disabled={activate.isPending} onClick={() => activateStage(model.recommendedNext!)}>
                {t(($) => $.issue_session.mark_as, { stage: t(($) => $.issue_session.stages[model.recommendedNext!]) })}
                <ArrowRight aria-hidden="true" />
              </Button>
            ) : null}
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    disabled={activate.isPending}
                    aria-label={t(($) => $.issue_session.record_another_stage)}
                    title={t(($) => $.issue_session.record_another_stage)}
                  >
                    <MoreHorizontal aria-hidden="true" />
                  </Button>
                }
              />
              <DropdownMenuContent align="end" className="min-w-44">
                {model.availableStages.map((kind) => <DropdownMenuItem key={kind} onClick={() => activateStage(kind)}>{t(($) => $.issue_session.stages[kind])}</DropdownMenuItem>)}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        )}
      </div>

      {model.hasContinuityGap && (
        <div role="status" className="mb-3 flex gap-2 rounded-lg border border-warning/40 bg-warning/10 p-3 text-caption">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
          <div><div className="font-medium text-foreground">{t(($) => $.issue_session.gap_title)}</div><p className="mt-0.5 text-muted-foreground">{t(($) => $.issue_session.gap_recovery)}</p></div>
        </div>
      )}
      {closed && <p role="status" className="mb-3 text-caption text-muted-foreground">{t(($) => $.issue_session.closed_hint)}</p>}
      {parked && !closed && <p role="status" className="mb-3 text-caption text-muted-foreground">{t(($) => $.issue_session.parked_hint)}</p>}
      {model.recommendedNext === null && !closed && <p className="mb-3 text-caption text-muted-foreground">{t(($) => $.issue_session.release_complete_hint)}</p>}
      {activate.isError && <p role="alert" className="mb-3 text-caption text-destructive">{t(($) => $.issue_session.stage_failed)}</p>}
      {tasksError && <p role="alert" className="mb-3 text-caption text-destructive">{t(($) => $.issue_session.conversation_load_failed)}</p>}

      <details className="border-t border-border/60 py-2.5 text-caption">
        <summary className="cursor-pointer select-none font-medium text-muted-foreground hover:text-foreground">{t(($) => $.issue_session.stage_details)}</summary>
        <div className="mt-3 space-y-4 text-muted-foreground">
          <div>
            <p>{t(($) => $.issue_session.stage_guidance[model.active.kind].description)}</p>
            <div className="mt-3 font-medium text-foreground">{t(($) => $.issue_session.expected_evidence)}</div>
            <ul className="mt-1.5 grid gap-1 sm:grid-cols-2">
              {(["primary", "secondary"] as const).map((key) => <li key={key} className="flex gap-2"><span aria-hidden="true" className="mt-[0.55em] h-1 w-1 shrink-0 rounded-full bg-muted-foreground" /><span>{t(($) => $.issue_session.stage_guidance[model.active.kind].evidence[key])}</span></li>)}
            </ul>
            <p className="mt-3 text-micro">{t(($) => $.issue_session.stage_hint)}</p>
          </div>

          {session.handoff && (
            <div className="border-t border-border/60 pt-3">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <div className="font-medium text-foreground">{t(($) => $.issue_session.inherited_context)}</div>
                  <div className="mt-1 text-body font-medium text-foreground">{session.handoff.objective}</div>
                  {session.handoff.source_stage_kind && <div className="mt-1 text-micro">{t(($) => $.issue_session.handoff_source, { stage: t(($) => $.issue_session.stages[session.handoff!.source_stage_kind!]) })}</div>}
                </div>
                <AppLink className="font-medium text-primary hover:underline" href={paths.issueDetail(session.handoff.parent_issue_id)}>{t(($) => $.issue_session.parent_link)}</AppLink>
              </div>
              {compactHandoff.length > 0 && <ul className="mt-2 list-disc space-y-1 pl-4">{compactHandoff.map((item, index) => <li key={index}>{item}</li>)}</ul>}
              <details className="mt-2">
                <summary className="cursor-pointer select-none font-medium hover:text-foreground">{t(($) => $.issue_session.full_handoff)}</summary>
                <div className="mt-3 grid gap-3 sm:grid-cols-2">
                  {([
                    ["confirmed_facts", session.handoff.confirmed_facts], ["decisions", session.handoff.decisions], ["constraints", session.handoff.constraints],
                    ["acceptance_criteria", session.handoff.acceptance_criteria], ["references", session.handoff.references], ["open_questions", session.handoff.open_questions],
                  ] as const).map(([key, values]) => values.length > 0 && <div key={key}><div className="font-medium text-foreground">{t(($) => $.issue_session.handoff_sections[key])}</div><ul className="mt-1 list-disc space-y-0.5 pl-4">{values.map((value, index) => <li key={`${key}-${index}`}>{value}</li>)}</ul></div>)}
                </div>
              </details>
            </div>
          )}

          {(model.occurrences.length > 1 || session.continuity_state !== "fresh") && (
            <div className="border-t border-border/60 pt-3">
              {model.occurrences.length > 1 && <div><span className="font-medium text-foreground">{t(($) => $.issue_session.history)}: </span>{model.occurrences.map((stage) => t(($) => $.issue_session.stages[stage.kind])).join(" → ")}</div>}
              {session.continuity_state !== "fresh" && <div className={model.occurrences.length > 1 ? "mt-1" : undefined}><span className="font-medium text-foreground">{t(($) => $.issue_session.continuity_label)}: </span>{t(($) => $.issue_session.continuity[session.continuity_state])}</div>}
            </div>
          )}
        </div>
      </details>
    </section>
  );
}
