import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { runtimeKeys } from "@multica/core/runtimes";
import type { AgentRuntime } from "@multica/core/types";
import { useDaemonIPCBridge } from "./daemon-ipc-bridge";

function runtime(id: string, daemonId: string, status: AgentRuntime["status"]): AgentRuntime {
  return {
    id, daemon_id: daemonId, workspace_id: "workspace", name: id,
    runtime_mode: "local", provider: id, launch_header: "", status,
    device_info: "", metadata: {}, owner_id: "owner", visibility: "private",
    last_seen_at: "2026-10-25T00:00:00Z", created_at: "2026-10-25T00:00:00Z",
    updated_at: "2026-10-25T00:00:00Z",
  };
}

const clients: QueryClient[] = [];
const originalDaemonAPI = Object.getOwnPropertyDescriptor(window, "daemonAPI");
afterEach(() => {
  for (const client of clients.splice(0)) client.clear();
  vi.restoreAllMocks();
  if (originalDaemonAPI) Object.defineProperty(window, "daemonAPI", originalDaemonAPI);
  else Reflect.deleteProperty(window, "daemonAPI");
});

function mountBridge() {
  const client = new QueryClient();
  clients.push(client);
  const rows = [runtime("codex", "local", "online"), runtime("claude", "local", "offline"), runtime("pi", "local", "offline"), runtime("remote", "remote", "online")];
  client.setQueryData(runtimeKeys.list("workspace"), rows);
  let emit: (status: { state: "running" | "stopped" | "stopping" | "recovery_paused" | "auth_expired" | "starting"; daemonId?: string }) => void = () => { throw new Error("not subscribed"); };
  const unsubscribe = vi.fn();
  const onStatusChange = vi.fn((listener: typeof emit) => { emit = listener; return unsubscribe; });
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: { onStatusChange } });
  const mounted = renderHook(() => useDaemonIPCBridge("workspace"), {
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  });
  return { client, rows, emit: (status: Parameters<typeof emit>[0]) => act(() => emit(status)), unsubscribe, ...mounted };
}

describe("useDaemonIPCBridge", () => {
  it("keeps unavailable providers offline on a running daemon", () => {
    const { client, rows, emit } = mountBridge();
    emit({ state: "running", daemonId: "local" });
    expect(client.getQueryData(runtimeKeys.list("workspace"))).toEqual(rows);
  });

  it.each(["stopped", "stopping", "recovery_paused", "auth_expired"] as const)("marks only a %s daemon offline and waits for server confirmation before restoring providers", (state) => {
    const { client, emit } = mountBridge();
    emit({ state, daemonId: "local" });
    expect(client.getQueryData<AgentRuntime[]>(runtimeKeys.list("workspace"))?.map((row) => row.status)).toEqual(["offline", "offline", "offline", "online"]);
    emit({ state: "running", daemonId: "local" });
    expect(client.getQueryData<AgentRuntime[]>(runtimeKeys.list("workspace"))?.map((row) => row.status)).toEqual(["offline", "offline", "offline", "online"]);
  });

  it("ignores unresolved/intermediate events and unsubscribes on unmount", () => {
    const { client, rows, emit, unmount, unsubscribe } = mountBridge();
    emit({ state: "running" });
    emit({ state: "starting", daemonId: "local" });
    expect(client.getQueryData(runtimeKeys.list("workspace"))).toEqual(rows);
    unmount();
    expect(unsubscribe).toHaveBeenCalledOnce();
  });
});
