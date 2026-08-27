// @vitest-environment node
import { describe, expect, it } from "vitest";
import { localDatabaseID, parseLoopbackAddress } from "./local-server-manager";

describe("parseLoopbackAddress", () => {
  it("normalizes supported loopback URLs", () => {
    expect(parseLoopbackAddress("http://localhost:17824")).toEqual({
      host: "127.0.0.1",
      port: "17824",
    });
    expect(parseLoopbackAddress("http://127.0.0.1:19090")).toEqual({
      host: "127.0.0.1",
      port: "19090",
    });
  });

  it("rejects remote, encrypted, and path-prefixed URLs", () => {
    expect(() => parseLoopbackAddress("https://127.0.0.1:17824")).toThrow();
    expect(() => parseLoopbackAddress("http://192.0.2.10:17824")).toThrow();
    expect(() => parseLoopbackAddress("http://127.0.0.1:17824/api")).toThrow();
  });
});

describe("localDatabaseID", () => {
  it("is deterministic and path-specific", () => {
    expect(localDatabaseID("/tmp/a.db")).toBe(localDatabaseID("/tmp/a.db"));
    expect(localDatabaseID("/tmp/a.db")).not.toBe(localDatabaseID("/tmp/b.db"));
  });
});
