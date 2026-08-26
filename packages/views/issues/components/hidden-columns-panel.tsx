"use client";

import { statusCategoryOfKey } from "@multica/core/issues";
import { ChevronDown, Eye } from "lucide-react";
import type { IssueStatusCategory } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import { StatusIcon } from "./status-icon";
import { useT } from "../../i18n";

/**
 * Single source of truth for the "Hidden columns" side panel rendered by
 * the kanban-style views (board and swimlane).
 *
 * Each consumer renders its own per-row count via the {@link renderRow} slot —
 * the board reads the server status facet's exact total, while the swimlane
 * uses an in-memory total derived from already-loaded issues. Centralising
 * the chrome here keeps a future view (calendar / timeline / etc.) from
 * forking yet another copy.
 */
export function HiddenColumnsPanel({
  hiddenStatuses,
  renderRow,
}: {
  hiddenStatuses: IssueStatusCategory[];
  renderRow: (status: IssueStatusCategory) => React.ReactNode;
}) {
  const { t } = useT("issues");
  return (
    <div className="flex shrink-0 items-start px-1">
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="text-muted-foreground"
              aria-label={t(($) => $.board.hidden_columns_count, {
                count: hiddenStatuses.length,
              })}
            >
              <Eye className="size-3.5" />
              {t(($) => $.board.hidden_columns_count, {
                count: hiddenStatuses.length,
              })}
              <ChevronDown className="size-3.5" />
            </Button>
          }
        />
        <DropdownMenuContent align="end" className="min-w-52">
          {hiddenStatuses.map((status) => renderRow(status))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

/**
 * One row inside {@link HiddenColumnsPanel}. Pure presentational — the caller
 * computes `total` however it likes (cached query vs. in-memory aggregate).
 */
export function HiddenColumnRow({
  status,
  total,
}: {
  status: IssueStatusCategory;
  total?: number;
}) {
  const { t } = useT("issues");
  const viewStoreApi = useViewStoreApi();
  const statusLabel = t(($) => $.status[statusCategoryOfKey(status)]);
  return (
    <DropdownMenuItem
      aria-label={`${t(($) => $.board.show_column)}: ${statusLabel}`}
      onClick={() => viewStoreApi.getState().showStatus(status)}
    >
      <StatusIcon status={status} className="size-3.5" />
      <span>{statusLabel}</span>
      {total !== undefined && (
        <span className="ml-auto text-caption text-muted-foreground">{total}</span>
      )}
    </DropdownMenuItem>
  );
}
