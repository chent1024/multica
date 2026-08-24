import { api } from "@multica/core/api";
import type { Workspace } from "@multica/core/types";

export interface LocalWorkbenchBootstrapResult {
  workspaces: Workspace[];
}

export function selectLocalWorkspaceList(
  workspaces: readonly Workspace[],
): Workspace[] {
  return workspaces.length === 1 ? [...workspaces] : [];
}

export async function bootstrapLocalWorkbench(
): Promise<LocalWorkbenchBootstrapResult> {
  const workspaces = selectLocalWorkspaceList(await api.listWorkspaces());
  if (workspaces.length !== 1) {
    throw new Error("local workbench must expose exactly one workspace");
  }
  return { workspaces };
}
