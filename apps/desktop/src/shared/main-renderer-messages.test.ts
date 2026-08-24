// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import {
  MainRendererMessageQueue,
  parseMainRendererChannelState,
} from "./main-renderer-messages";

describe("MainRendererMessageQueue", () => {
  it("holds messages until their matching listener is ready", () => {
    const queue = new MainRendererMessageQueue();
    const send = vi.fn();

    queue.enqueue("settings:open", undefined, send);
    queue.setReady("settings:open", true, send);
    expect(send).toHaveBeenCalledWith("settings:open", undefined);
  });

  it("delivers immediately while a channel is ready", () => {
    const queue = new MainRendererMessageQueue();
    const send = vi.fn();

    queue.setReady("settings:open", true, send);
    queue.enqueue("settings:open", undefined, send);

    expect(send).toHaveBeenCalledOnce();
  });

  it("keeps queued work across a renderer readiness reset", () => {
    const queue = new MainRendererMessageQueue();
    const send = vi.fn();

    queue.setReady("settings:open", true, send);
    queue.resetReady();
    queue.enqueue("settings:open", undefined, send);
    expect(send).not.toHaveBeenCalled();

    queue.setReady("settings:open", true, send);
    expect(send).toHaveBeenCalledWith("settings:open", undefined);
  });

});

describe("parseMainRendererChannelState", () => {
  it("accepts only allowlisted channels with an explicit boolean", () => {
    expect(
      parseMainRendererChannelState({ channel: "settings:open", ready: true }),
    ).toEqual({ channel: "settings:open", ready: true });
    expect(
      parseMainRendererChannelState({ channel: "shell:openExternal", ready: true }),
    ).toBeNull();
    expect(
      parseMainRendererChannelState({ channel: "settings:open", ready: "yes" }),
    ).toBeNull();
  });
});
