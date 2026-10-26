// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook } from "@testing-library/react";
import { useLogout } from "./use-logout";

// What client state gets erased, and the order it has to happen in, is pinned
// in core's platform/session-cleanup.test.ts — the session-expiry path runs
// the same function. What is left for this hook is the logout-only tail:
// erase, then drop auth, then move the URL.
const calls = vi.hoisted(() => [] as string[]);
const mockClearClientSessionData = vi.hoisted(() => vi.fn());
const mockAuthLogout = vi.hoisted(() => vi.fn());
const mockPush = vi.hoisted(() => vi.fn());
const desktopShell = vi.hoisted(() => ({ current: false }));

vi.mock("../platform/local-directory", () => ({
  isDesktopShell: () => desktopShell.current,
}));
const queryClient = vi.hoisted(() => ({ id: "query-client" }));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => queryClient,
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: (s: unknown) => unknown) => {
      const state = { logout: mockAuthLogout };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ logout: mockAuthLogout }) },
  ),
}));

vi.mock("@multica/core/platform", () => ({
  clearClientSessionData: mockClearClientSessionData,
}));

vi.mock("@multica/core/paths", () => ({
  paths: { login: () => "/login" },
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: mockPush }),
}));

describe("useLogout", () => {
  beforeEach(() => {
	desktopShell.current = false;
    calls.length = 0;
    vi.clearAllMocks();
    mockClearClientSessionData.mockImplementation(() => calls.push("clear"));
    mockAuthLogout.mockImplementation(() => calls.push("authLogout"));
    mockPush.mockImplementation(() => calls.push("push"));
  });

  it("keeps the fixed local operator authenticated in Desktop", () => {
    desktopShell.current = true;
    const { result } = renderHook(() => useLogout());
    result.current();

    expect(mockClearClientSessionData).not.toHaveBeenCalled();
    expect(mockAuthLogout).not.toHaveBeenCalled();
    expect(mockPush).not.toHaveBeenCalled();
  });

  // Erasing after the auth store publishes `unauthenticated` would race the
  // shells' login redirect, and navigating first would leave the caller on a
  // workspace URL that renders null.
  it("erases client state, then drops auth, then navigates to /login", () => {
    const { result } = renderHook(() => useLogout());
    result.current();

    expect(calls).toEqual(["clear", "authLogout", "push"]);
    expect(mockClearClientSessionData).toHaveBeenCalledWith(queryClient);
    expect(mockPush).toHaveBeenCalledWith("/login");
  });
});
