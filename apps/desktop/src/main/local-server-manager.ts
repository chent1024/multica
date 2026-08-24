import { app } from "electron";
import { spawn, type ChildProcess } from "child_process";
import { createHash } from "crypto";
import { createWriteStream, existsSync } from "fs";
import { mkdir } from "fs/promises";
import { join, resolve } from "path";

let child: ChildProcess | null = null;

function bundledServerPath(): string {
  const name =
    process.platform === "win32"
      ? "multica-server.exe"
      : "multica-server";
  return join(app.getAppPath(), "resources", "bin", name).replace(
    "app.asar",
    "app.asar.unpacked",
  );
}

function databaseID(databasePath: string): string {
  return createHash("sha256").update(resolve(databasePath)).digest("hex");
}

async function healthy(
  apiUrl: string,
  expectedDatabaseID: string,
): Promise<boolean> {
  try {
    const response = await fetch(`${apiUrl.replace(/\/+$/, "")}/health`, {
      signal: AbortSignal.timeout(1_000),
    });
    if (!response.ok) return false;
    const body = (await response.json()) as {
      status?: unknown;
      storage?: unknown;
      mode?: unknown;
      database_id?: unknown;
    };
    return (
      body.status === "ok" &&
      body.storage === "sqlite" &&
      body.mode === "local" &&
      body.database_id === expectedDatabaseID
    );
  } catch {
    return false;
  }
}

export async function startLocalServer(apiUrl: string): Promise<void> {
  const url = new URL(apiUrl);
  if (
    url.protocol !== "http:" ||
    (url.hostname !== "localhost" && url.hostname !== "127.0.0.1")
  ) {
    throw new Error("Local workbench API must use a loopback HTTP URL");
  }
  const port = url.port || "80";
  const dataDir = join(app.getPath("userData"), "data");
  const databasePath = join(dataDir, "multica.db");
  const expectedDatabaseID = databaseID(databasePath);
  if (await healthy(apiUrl, expectedDatabaseID)) return;

  const binary = bundledServerPath();
  if (!existsSync(binary)) {
    throw new Error(`Bundled local server is missing: ${binary}`);
  }

  const logDir = join(app.getPath("userData"), "logs");
  await mkdir(dataDir, { recursive: true });
  await mkdir(logDir, { recursive: true });
  const log = createWriteStream(join(logDir, "local-server.log"), { flags: "a" });
  child = spawn(binary, [], {
    env: {
      ...process.env,
      PORT: port,
      MULTICA_SQLITE_PATH: databasePath,
    },
    stdio: ["ignore", "ignore", "pipe"],
    windowsHide: true,
  });
  child.stderr?.pipe(log);
  child.once("exit", () => {
    child = null;
    log.end();
  });

  for (let attempt = 0; attempt < 80; attempt += 1) {
    if (await healthy(apiUrl, expectedDatabaseID)) return;
    if (!child) break;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  child?.kill();
  child = null;
  throw new Error("Local SQLite server did not become ready");
}

export function stopLocalServer(): void {
  child?.kill();
  child = null;
}
