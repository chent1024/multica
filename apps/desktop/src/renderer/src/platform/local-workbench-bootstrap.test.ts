// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Workspace } from "@multica/core/types";

const mocks = vi.hoisted(() => {
  return {
    listWorkspaces: vi.fn(),
  };
});

vi.mock("@multica/core/api", () => ({
  api: {
    listWorkspaces: mocks.listWorkspaces,
  },
}));

import {
  bootstrapLocalWorkbench,
  selectLocalWorkspaceList,
} from "./local-workbench-bootstrap";

function workspace(id: string, slug: string): Workspace {
  return {
    id,
    name: "Local",
    slug,
    description: null,
    context: null,
    settings: {},
    repos: [],
    issue_prefix: "LOC",
    avatar_url: null,
    created_at: "2026-08-24T00:00:00Z",
    updated_at: "2026-08-24T00:00:00Z",
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("bootstrapLocalWorkbench", () => {
  it("rejects a non-singleton workspace list", () => {
    const later = workspace("workspace-2", "later");
    later.created_at = "2026-08-25T00:00:00Z";
    const earlier = workspace("workspace-1", "earlier");

    expect(selectLocalWorkspaceList([later, earlier])).toEqual([]);
    expect(selectLocalWorkspaceList([])).toEqual([]);
  });

  it("uses the one server-owned local workspace", async () => {
    const local = workspace("workspace-1", "local");
    mocks.listWorkspaces.mockResolvedValue([local]);

    const result = await bootstrapLocalWorkbench();

    expect(result.workspaces).toEqual([local]);
  });

  it("fails closed if the server exposes multiple workspaces", async () => {
    const first = workspace("workspace-1", "local");
    const legacy = workspace("workspace-2", "legacy-cloud");
    mocks.listWorkspaces.mockResolvedValue([first, legacy]);

    await expect(bootstrapLocalWorkbench()).rejects.toThrow(
      "local workbench must expose exactly one workspace",
    );
  });

  it("fails when the server has not initialized its workspace", async () => {
    mocks.listWorkspaces.mockResolvedValue([]);

    await expect(bootstrapLocalWorkbench()).rejects.toThrow(
      "local workbench must expose exactly one workspace",
    );
  });
});
