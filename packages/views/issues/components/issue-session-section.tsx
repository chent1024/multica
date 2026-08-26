"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowRight, ChevronDown } from "lucide-react";
import type { AgentTask, Issue, IssueSessionStageKind, TimelineEntry } from "@multica/core/types";
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
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { buildIssueSessionWorkspaceModel } from "./issue-session-workspace-model";
import { TranscriptButton } from "../../common/task-transcript";

export function IssueSessionWorkspace({ issue, timeline }: { issue: Issue; timeline: TimelineEntry[] }) {
  const issueId = issue.id;
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const { data: session, isLoading, isError, refetch } = useQuery({
    ...issueSessionOptions(issueId),
  });
  const { data: tasks = [], isError: tasksError } = useQuery({
    queryKey: issueKeys.tasks(issueId),
    queryFn: () => api.listTasksByIssue(issueId),
  });
  const activate = useActivateIssueSessionStage(issueId);
  const [openTask, setOpenTask] = useState<AgentTask | null>(null);

  if (isLoading) {
    return (
      <section
        data-issue-session-workspace
        className="mt-8 rounded-xl border bg-card/30 px-4 py-4 text-body text-muted-foreground"
      >
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
        timeline,
      )
    : null;

  if (isError || !session || !model) {
    return (
      <section
        data-issue-session-workspace
        role="alert"
        className="mt-8 rounded-xl border border-destructive/30 bg-destructive/5 px-4 py-4 text-body text-destructive"
      >
        <p>{t(($) => $.issue_session.load_failed)}</p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="mt-3"
          onClick={() => void refetch()}
        >
          {t(($) => $.issue_session.retry)}
        </Button>
      </section>
    );
  }

  const activateStage = (kind: IssueSessionStageKind) => {
    activate.mutate(kind);
  };
  const statusCategory = issueStatusCategory(issue);
  const closed = statusCategory === "done" || statusCategory === "cancelled";
  const parked = statusCategory === "backlog";
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
    <section
      data-issue-session-workspace
      aria-labelledby="issue-current-work-title"
      className="mt-8 overflow-hidden rounded-xl border bg-card/30"
    >
      <div className="p-4 md:p-5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">
            {t(($) => $.issue_session.current_work)}
          </span>
          <span className="rounded-full border border-primary/30 bg-primary/10 px-2 py-0.5 text-caption font-medium text-primary">
            {t(($) => $.issue_session.stages[model.active.kind])}
          </span>
        </div>

        <h2 id="issue-current-work-title" className="mt-2 text-title-sm font-semibold text-foreground">
          {t(($) => $.issue_session.stage_guidance[model.active.kind].goal)}
        </h2>
        <p className="mt-1 text-body text-muted-foreground">
          {t(($) => $.issue_session.stage_guidance[model.active.kind].description)}
        </p>

        <div className="mt-4 rounded-lg border bg-background/60 p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-caption font-medium text-foreground">
              {t(($) => $.issue_session.latest_result)}
            </div>
            {model.latestStageTask && (
              <span className="text-micro text-muted-foreground">
                {t(($) => $.issue_session.run_summary, {
                  count: model.stageRunCount,
                  status: t(($) => $.issue_session.run_status[model.latestStageTask!.status as keyof typeof $.issue_session.run_status]),
                })}
              </span>
            )}
          </div>
          <p className="mt-1 line-clamp-3 text-caption text-muted-foreground">
            {model.latestOutcome ?? t(($) => $.issue_session.no_result)}
          </p>
          {model.latestStageTask && !model.activeTask && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="mt-2 -ml-2"
              onClick={() => setOpenTask(model.latestStageTask)}
            >
              {t(($) => $.issue_session.view_latest_conversation)}
            </Button>
          )}
        </div>

        <div className="mt-4">
          <div className="text-caption font-medium text-foreground">
            {t(($) => $.issue_session.expected_evidence)}
          </div>
          <ul className="mt-1.5 grid gap-1 text-caption text-muted-foreground sm:grid-cols-2">
            {(["primary", "secondary"] as const).map((key) => (
              <li key={key} className="flex gap-2">
                <span aria-hidden="true" className="mt-[0.55em] h-1 w-1 shrink-0 rounded-full bg-muted-foreground" />
                <span>{t(($) => $.issue_session.stage_guidance[model.active.kind].evidence[key])}</span>
              </li>
            ))}
          </ul>
        </div>

        {model.hasContinuityGap && (
          <div role="status" className="mt-4 flex gap-2 rounded-lg border border-warning/40 bg-warning/10 p-3 text-caption">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
            <div>
              <div className="font-medium text-foreground">{t(($) => $.issue_session.gap_title)}</div>
              <p className="mt-0.5 text-muted-foreground">{t(($) => $.issue_session.gap_recovery)}</p>
            </div>
          </div>
        )}

        {parked && !closed && (
          <p role="status" className="mt-4 text-caption text-muted-foreground">
            {t(($) => $.issue_session.parked_hint)}
          </p>
        )}

        <div className="mt-4 flex flex-wrap items-center gap-2">
          {closed && (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.issue_session.closed_hint)}
            </span>
          )}
          {model.recommendedNext && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={activate.isPending}
              onClick={() => activateStage(model.recommendedNext!)}
            >
              {t(($) => $.issue_session.mark_as, {
                stage: t(($) => $.issue_session.stages[model.recommendedNext!]),
              })}
              <ArrowRight aria-hidden="true" />
            </Button>
          )}
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button type="button" size="sm" variant="outline" disabled={activate.isPending}>
                  {t(($) => $.issue_session.record_another_stage)}
                  <ChevronDown aria-hidden="true" />
                </Button>
              }
            />
            <DropdownMenuContent align="start" className="min-w-44">
              {model.availableStages.map((kind) => (
                <DropdownMenuItem key={kind} onClick={() => activateStage(kind)}>
                  {t(($) => $.issue_session.stages[kind])}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          {model.recommendedNext === null && (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.issue_session.release_complete_hint)}
            </span>
          )}
        </div>

        <p className="mt-2 text-micro text-muted-foreground">
          {t(($) => $.issue_session.stage_hint)}
        </p>
        {activate.isError && (
          <p role="alert" className="mt-2 text-caption text-destructive">
            {t(($) => $.issue_session.stage_failed)}
          </p>
        )}
        {tasksError && (
          <p role="alert" className="mt-2 text-caption text-destructive">
            {t(($) => $.issue_session.conversation_load_failed)}
          </p>
        )}
        {openTask && (
          <TranscriptButton
            task={openTask}
            agentName=""
            isLive={["queued", "dispatched", "waiting_local_directory", "waiting_previous_run", "running"].includes(openTask.status)}
            renderButton={false}
            open
            onOpenChange={(open) => { if (!open) setOpenTask(null); }}
          />
        )}
      </div>

      {session.handoff && (
        <div className="border-t bg-muted/20 px-4 py-4 md:px-5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <div className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">
                {t(($) => $.issue_session.inherited_context)}
              </div>
              <div className="mt-1 text-body font-medium text-foreground">
                {session.handoff.objective}
              </div>
              {session.handoff.source_stage_kind && (
                <div className="mt-1 text-micro text-muted-foreground">
                  {t(($) => $.issue_session.handoff_source, {
                    stage: t(($) => $.issue_session.stages[session.handoff!.source_stage_kind!]),
                  })}
                </div>
              )}
            </div>
            <AppLink className="text-caption font-medium text-primary hover:underline" href={paths.issueDetail(session.handoff.parent_issue_id)}>
              {t(($) => $.issue_session.parent_link)}
            </AppLink>
          </div>
          {compactHandoff.length > 0 && (
            <ul className="mt-3 list-disc space-y-1 pl-4 text-caption text-muted-foreground">
              {compactHandoff.map((item, index) => <li key={index}>{item}</li>)}
            </ul>
          )}
          <details className="mt-3 text-caption">
            <summary className="cursor-pointer select-none font-medium text-muted-foreground hover:text-foreground">
              {t(($) => $.issue_session.full_handoff)}
            </summary>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            {([
              ["confirmed_facts", session.handoff.confirmed_facts],
              ["decisions", session.handoff.decisions],
              ["constraints", session.handoff.constraints],
              ["acceptance_criteria", session.handoff.acceptance_criteria],
              ["references", session.handoff.references],
              ["open_questions", session.handoff.open_questions],
            ] as const).map(([key, items]) => items.length > 0 && (
              <div key={key}>
                <div className="text-caption font-medium text-foreground">
                  {t(($) => $.issue_session.handoff_sections[key])}
                </div>
                <ul className="mt-1 list-disc space-y-0.5 pl-4 text-caption text-muted-foreground">
                  {items.map((item, index) => <li key={`${key}-${index}`}>{item}</li>)}
                </ul>
              </div>
            ))}
          </div>
          </details>
        </div>
      )}

      <details className="border-t px-4 py-3 text-caption md:px-5">
        <summary className="cursor-pointer select-none font-medium text-muted-foreground hover:text-foreground">
          {t(($) => $.issue_session.journey_details)}
        </summary>
        <div className="mt-3 space-y-2 text-muted-foreground">
          <div>
            <span className="font-medium text-foreground">{t(($) => $.issue_session.history)}: </span>
            {model.occurrences.map((stage) => t(($) => $.issue_session.stages[stage.kind])).join(" → ")}
          </div>
          <div>
            <span className="font-medium text-foreground">{t(($) => $.issue_session.continuity_label)}: </span>
            {t(($) => $.issue_session.continuity[session.continuity_state])}
          </div>
        </div>
      </details>
    </section>
  );
}
