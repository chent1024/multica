import { describe, expect, it } from "vitest";
import { matchRoutes } from "react-router-dom";
import { appRoutes } from "./routes";

describe("local desktop routes", () => {
  it("keeps links to the local Codex agent inside the agent surface", () => {
    const matches = matchRoutes(
      appRoutes,
      "/local/agents/00000000-0000-4000-8000-000000000002",
    );

    expect(matches?.at(-1)?.route.path).toBe("agents/:id");
  });
});
