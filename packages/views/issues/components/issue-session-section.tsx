"use client";

import { useQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { useState } from "react";
import type { IssueSessionStageKind } from "@multica/core/types";
import { issueSessionOptions, useActivateIssueSessionStage } from "@multica/core/issues/session";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { useWorkspacePaths } from "@multica/core/paths";

const stageKinds: IssueSessionStageKind[] = [
  "analysis", "implementation", "review", "test", "commit", "push", "release",
];

export function IssueSessionSection({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(true);
	const paths = useWorkspacePaths();
  const { data: session, isLoading, isError } = useQuery({
	...issueSessionOptions(issueId),
  });
	const activate = useActivateIssueSessionStage(issueId);

	if (isLoading) return <div className="px-2 py-1 text-caption text-muted-foreground">{t(($) => $.issue_session.loading)}</div>;
	if (isError || !session) return <div className="px-2 py-1 text-caption text-destructive">{t(($) => $.issue_session.load_failed)}</div>;
  const active = session.stages.find((stage) => stage.id === session.active_stage_id);

  return (
    <section>
      <button
        type="button"
        className={`mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70 ${open ? "" : "text-muted-foreground"}`}
        onClick={() => setOpen((value) => !value)}
      >
        {t(($) => $.issue_session.section)}
        <ChevronRight className={`!size-3 text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`} />
        <span className="ml-auto font-normal text-muted-foreground">
          {t(($) => $.issue_session.continuity[session.continuity_state])}
        </span>
      </button>
      {open && (
        <div className="space-y-2 pl-2">
          <div className="flex flex-wrap gap-1" aria-label={t(($) => $.issue_session.stages_label)}>
            {stageKinds.map((kind) => (
              <button
                key={kind}
                type="button"
                disabled={activate.isPending}
                onClick={() => activate.mutate(kind)}
                className={`rounded-full border px-2 py-0.5 text-caption transition-colors hover:bg-accent ${active?.kind === kind ? "border-primary bg-primary/10 text-primary" : "text-muted-foreground"}`}
              >
                {t(($) => $.issue_session.stages[kind])}
              </button>
            ))}
          </div>
		  <div className="text-micro text-muted-foreground">
			  <span className="font-medium">{t(($) => $.issue_session.history)}: </span>
			  {session.stages.map((stage) => t(($) => $.issue_session.stages[stage.kind])).join(" → ")}
		  </div>
		  <p className="text-micro text-muted-foreground">{t(($) => $.issue_session.stage_hint)}</p>
		  {activate.isError && <p className="text-caption text-destructive">{t(($) => $.issue_session.stage_failed)}</p>}
          {session.handoff && (
            <div className="rounded-md border bg-muted/30 p-2 text-caption">
			  <div className="flex items-center justify-between gap-2">
				  <div className="font-medium">{t(($) => $.issue_session.handoff)}</div>
				  <AppLink className="text-primary hover:underline" href={paths.issueDetail(session.handoff.parent_issue_id)}>
					  {t(($) => $.issue_session.parent_link)}
				  </AppLink>
			  </div>
              <p className="mt-1 text-muted-foreground">{session.handoff.objective}</p>
			  {([
				  ["confirmed_facts", session.handoff.confirmed_facts], ["decisions", session.handoff.decisions],
				  ["constraints", session.handoff.constraints], ["acceptance_criteria", session.handoff.acceptance_criteria],
				  ["references", session.handoff.references], ["open_questions", session.handoff.open_questions],
			  ] as const).map(([key, items]) => items.length > 0 && (
				  <div key={key} className="mt-2">
					  <div className="font-medium">{t(($) => $.issue_session.handoff_sections[key])}</div>
					  <ul className="list-disc pl-4 text-muted-foreground">{items.map((item, index) => <li key={`${key}-${index}`}>{item}</li>)}</ul>
				  </div>
			  ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
