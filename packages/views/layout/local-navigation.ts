import type { WorkspacePageKey } from "@multica/core/paths";
import type { ShortcutActionId } from "@multica/core/shortcuts";

export const LOCAL_PAGE_KEYS = new Set<WorkspacePageKey>([
  "issues",
  "projects",
  "runtimes",
  "settings",
]);

export const LOCAL_SHORTCUT_ACTION_IDS = new Set<ShortcutActionId>([
  "openSearch",
  "createIssue",
  "toggleSidebar",
  "toggleRightSidebar",
  "findInIssue",
  "openThreadNav",
  "send",
  "goBack",
  "goForward",
  "goIssues",
  "goProjects",
  "goRuntimes",
  "goSettings",
]);
