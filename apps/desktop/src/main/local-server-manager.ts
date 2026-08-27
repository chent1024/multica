import { app } from "electron";
import { spawn, type ChildProcess } from "child_process";
import { createHash } from "crypto";
import { createWriteStream, existsSync, type WriteStream } from "fs";
import { mkdir } from "fs/promises";
import { join, resolve } from "path";

let child: ChildProcess | null = null;
let log: WriteStream | null = null;
let restartTimer: NodeJS.Timeout | null = null;
let stopRequested = false;
let activeApiUrl: string | null = null;

export function localDatabaseID(databasePath: string): string {
  return createHash("sha256").update(resolve(databasePath)).digest("hex");
}

export function parseLoopbackAddress(apiUrl: string): { host: string; port: string } {
  const url = new URL(apiUrl);
  if (
    url.protocol !== "http:" ||
    (url.hostname !== "localhost" &&
      url.hostname !== "127.0.0.1" &&
      url.hostname !== "[::1]")
  ) {
    throw new Error("Local workbench API must use a loopback HTTP URL");
  }
  if (url.pathname !== "/" || url.search || url.hash) {
    throw new Error("Local workbench API URL must not include a path, query, or fragment");
  }
  return { host: url.hostname === "localhost" ? "127.0.0.1" : url.hostname, port: url.port || "80" };
}

function bundledServerPath(): string {
  const name = process.platform === "win32" ? "multica-server.exe" : "multica-server";
  return join(app.getAppPath(), "resources", "bin", name).replace(
    "app.asar",
    "app.asar.unpacked",
  );
}

async function healthState(
  apiUrl: string,
  expectedDatabaseID: string,
): Promise<"match" | "other" | "unavailable"> {
  try {
    const response = await fetch(`${apiUrl.replace(/\/+$/, "")}/health`, {
      signal: AbortSignal.timeout(1_000),
    });
    if (!response.ok) return "unavailable";
    const body = (await response.json()) as Record<string, unknown>;
    if (body.status !== "ok" || body.mode !== "local" || body.storage !== "sqlite") {
      return "other";
    }
    return body.database_id === expectedDatabaseID ? "match" : "other";
  } catch {
    return "unavailable";
  }
}

export async function startLocalServer(apiUrl: string): Promise<void> {
  stopRequested = false;
  activeApiUrl = apiUrl;
  const { host, port } = parseLoopbackAddress(apiUrl);
  const dataDir = join(app.getPath("userData"), "data");
  const databasePath = join(dataDir, "multica.db");
  const fileRoot = join(dataDir, "files");
  const expectedDatabaseID = localDatabaseID(databasePath);
  const initialHealth = await healthState(apiUrl, expectedDatabaseID);
  if (initialHealth === "match") return;
  if (initialHealth === "other") {
    throw new Error("Local API port is owned by a different server or data directory");
  }

  const binary = bundledServerPath();
  if (!existsSync(binary)) {
    throw new Error(`Bundled local server is missing: ${binary}`);
  }
  const logDir = join(app.getPath("userData"), "logs");
  await mkdir(dataDir, { recursive: true });
  await mkdir(fileRoot, { recursive: true });
  await mkdir(logDir, { recursive: true });
  log = createWriteStream(join(logDir, "local-server.log"), { flags: "a" });
  child = spawn(binary, [], {
    env: {
      ...process.env,
      MULTICA_LOCAL_ADDR: `${host}:${port}`,
      MULTICA_SQLITE_PATH: databasePath,
      MULTICA_FILE_ROOT: fileRoot,
    },
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
  });
  child.stdout?.pipe(log);
  child.stderr?.pipe(log);
  child.once("exit", () => {
    child = null;
    log?.end();
    log = null;
    if (!stopRequested && activeApiUrl) {
      const restartUrl = activeApiUrl;
      restartTimer = setTimeout(() => {
        restartTimer = null;
        void startLocalServer(restartUrl).catch((error: unknown) => {
          console.error("Failed to restart local SQLite server", error);
        });
      }, 500);
    }
  });

  for (let attempt = 0; attempt < 80; attempt += 1) {
    const state = await healthState(apiUrl, expectedDatabaseID);
    if (state === "match") return;
    if (state === "other") break;
    if (!child) break;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  stopLocalServer();
  throw new Error("Local SQLite server did not become ready for this data directory");
}

export function stopLocalServer(): void {
  stopRequested = true;
  activeApiUrl = null;
  if (restartTimer) {
    clearTimeout(restartTimer);
    restartTimer = null;
  }
  child?.kill();
  child = null;
  log?.end();
  log = null;
}
