import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NotificationsTab } from "./notifications-tab";

const { mutate } = vi.hoisted(() => ({ mutate: vi.fn() }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: { workspace_id: "local", preferences: {} } }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "local" }));
vi.mock("@multica/core/notification-preferences/queries", () => ({
  notificationPreferenceOptions: () => ({ queryKey: ["notification-preferences", "local"] }),
}));
vi.mock("@multica/core/notification-preferences/mutations", () => ({
  useUpdateNotificationPreferences: () => ({ mutate }),
}));
vi.mock("../../platform/local-directory", () => ({ isDesktopShell: () => true }));
vi.mock("./browser-notification-setting", () => ({ BrowserNotificationSetting: () => null }));
vi.mock("../../i18n", () => ({
  useT: () => ({
    t: (selector: (value: unknown) => unknown) => {
      const pathProxy = (path: string[]): unknown => new Proxy(() => undefined, {
        get: (_target, key) => key === Symbol.toPrimitive || key === "toString"
          ? () => path.join(".")
          : pathProxy([...path, String(key)]),
      });
      return String(selector(pathProxy([])));
    },
  }),
}));

describe("NotificationsTab local Desktop", () => {
  it("shows the local delivery mode and hides collaboration groups", () => {
    render(<NotificationsTab />);
    expect(screen.getByText("notifications.local.action_required_label")).toBeInTheDocument();
    expect(screen.queryByText("notifications.groups.assignments.label")).not.toBeInTheDocument();
    expect(screen.queryByText("notifications.groups.mentions.label")).not.toBeInTheDocument();
    expect(screen.getByText("notifications.system.label")).toBeInTheDocument();
  });
});
