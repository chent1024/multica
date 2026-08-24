import { useQuery } from "@tanstack/react-query";
import { Bot, RefreshCw } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions } from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";

/**
 * Local mode exposes the one Codex execution target as runtime state, not as
 * a user-owned collaborative agent. Keep this page intentionally read-only:
 * creation, ownership, access control, integrations, and team membership are
 * cloud concepts and their shared routes do not exist in Desktop.
 */
export function DesktopAgentsPage() {
  const workspaceId = useWorkspaceId();
  const agentsQuery = useQuery(agentListOptions(workspaceId));
  const runtimesQuery = useQuery(runtimeListOptions(workspaceId));
  const runtimesById = new Map(
    (runtimesQuery.data ?? []).map((runtime) => [runtime.id, runtime]),
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <header className="flex items-center justify-between border-b px-6 py-4">
        <div>
          <h1 className="text-title font-semibold">Local agents</h1>
          <p className="mt-1 text-body text-muted-foreground">
            Codex execution targets registered on this machine.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={agentsQuery.isFetching || runtimesQuery.isFetching}
          onClick={() => {
            void Promise.all([agentsQuery.refetch(), runtimesQuery.refetch()]);
          }}
        >
          <RefreshCw
            className={
              agentsQuery.isFetching || runtimesQuery.isFetching
                ? "animate-spin"
                : undefined
            }
          />
          Refresh
        </Button>
      </header>

      <div className="p-6">
        {agentsQuery.isPending ? (
          <div className="text-body text-muted-foreground">
            Loading local Codex runtime…
          </div>
        ) : agentsQuery.isError ? (
          <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-4">
            <p className="text-body font-medium text-destructive">
              Local Codex runtime is unavailable.
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {agentsQuery.error instanceof Error
                ? agentsQuery.error.message
                : "Unable to load local agents."}
            </p>
          </div>
        ) : (agentsQuery.data ?? []).length === 0 ? (
          <div className="rounded-lg border border-dashed p-8 text-center">
            <Bot className="mx-auto size-8 text-muted-foreground" />
            <p className="mt-3 text-body font-medium">
              No local Codex runtime registered
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              Start the local daemon from Settings, then refresh this page.
            </p>
          </div>
        ) : (
          <div className="grid gap-3">
            {(agentsQuery.data ?? []).map((agent) => {
              const runtime = runtimesById.get(agent.runtime_id);
              const online = runtime?.status === "online";
              return (
                <div
                  key={agent.id}
                  className="flex items-center gap-4 rounded-lg border bg-card p-4"
                >
                  <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-muted">
                    <Bot className="size-5" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-body font-medium">
                        {agent.name}
                      </span>
                      <span
                        className={`size-2 rounded-full ${online ? "bg-success" : "bg-muted-foreground/40"}`}
                        aria-label={online ? "online" : "offline"}
                      />
                    </div>
                    <p className="mt-1 truncate text-caption text-muted-foreground">
                      {runtime?.name ?? "Local Codex runtime"}
                    </p>
                  </div>
                  <span className="text-caption capitalize text-muted-foreground">
                    {online ? "online" : "offline"}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
