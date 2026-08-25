import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CoreProvider } from "@multica/core/platform";
import { pickLocale, type SupportedLocale } from "@multica/core/i18n";
import {
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import type { Workspace } from "@multica/core/types";
import { ThemeProvider } from "@multica/ui/components/common/theme-provider";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { Toaster } from "@multica/ui/components/ui/sonner";
import { LocalWorkbenchRecoveryPage } from "./pages/local-workbench-recovery";
import { DesktopShell } from "./components/desktop-layout";
import { UpdateNotification } from "./components/update-notification";
import { IssueWindow } from "./components/issue-window";
import { useTabStore } from "./stores/tab-store";
import { useOpenSettingsShortcut } from "./hooks/use-open-settings-shortcut";
import { useDaemonIPCBridge } from "./platform/daemon-ipc-bridge";
import { createDesktopLocaleAdapter } from "./platform/i18n-adapter";
import { captureEvent } from "@multica/core/analytics";
import { RESOURCES } from "@multica/views/locales";
import { DiagnosticRouteReporter } from "./platform/diagnostic-route-reporter";
import { flushFreezeBreadcrumb } from "./freeze-flush";
import {
  bootstrapLocalWorkbench,
  selectLocalWorkspaceList,
} from "./platform/local-workbench-bootstrap";
import { isLoopbackApiUrl } from "../../shared/runtime-config";

// BCP-47 region tags for the <html lang> attribute, mirroring
// apps/web/app/layout.tsx HTML_LANG. index.html ships a static lang="en";
// we sync it to the resolved locale at boot so screen readers announce the
// right language AND the Japanese-scoped CJK font override in globals.css
// (`html[lang|="ja"]`) can take effect.
const HTML_LANG: Record<SupportedLocale, string> = {
  en: "en",
  "zh-Hans": "zh-CN",
  ko: "ko-KR",
  ja: "ja-JP",
};

const EMPTY_LOCAL_WORKSPACES: Workspace[] = [];

/**
 * Cmd/Ctrl+W: close the active tab. When the last real tab is closed
 * (or no tabs/workspace exist during local bootstrap), close the window.
 *
 * Mounted at the App root so loading and runtime-config error states have a working Cmd+W
 * handler. Without this, states outside the tab shell would swallow the
 * shortcut and do nothing.
 */
function useCmdWCloseTab() {
  useEffect(() => {
    return window.desktopAPI.onCloseActiveTab(() => {
      if (window.desktopAPI.windowContext?.kind === "issue") {
        window.desktopAPI.closeWindow();
        return;
      }
      const store = useTabStore.getState();
      const { activeWorkspaceSlug, byWorkspace } = store;
      if (!activeWorkspaceSlug) {
        // No workspace — nothing to close, dismiss the window.
        window.desktopAPI.closeWindow();
        return;
      }
      const group = byWorkspace[activeWorkspaceSlug];
      if (!group || group.tabs.length <= 1) {
        // Last tab (or no tabs) — close the window.
        window.desktopAPI.closeWindow();
        return;
      }
      // Multiple tabs — close the active one.
      store.closeActiveTab();
    });
  }, []);
}

function IssueWindowContent() {
  const context = window.desktopAPI.windowContext ?? { kind: "main" as const };

  if (context.kind !== "issue") return null;
  return <IssueWindow context={context} />;
}

function AppContent() {
  const qc = useQueryClient();

  const [localBootstrapStatus, setLocalBootstrapStatus] = useState<
    "idle" | "loading" | "ready" | "failed"
  >("idle");
  const [localBootstrapGeneration, setLocalBootstrapGeneration] = useState(0);
  const localBootstrapRequestRef = useRef(0);

  const runtimeConfig = window.desktopAPI.runtimeConfig.ok
    ? window.desktopAPI.runtimeConfig.config
    : null;

  // Tell the main process which backend URL we talk to, so daemon-manager
  // can pick the matching CLI profile (server_url from ~/.multica config).
  useEffect(() => {
    if (!runtimeConfig) return;
    window.daemonAPI.setTargetApiUrl(runtimeConfig.apiUrl);
  }, [runtimeConfig]);

  useEffect(() => {
    if (
      localBootstrapStatus !== "idle"
    ) {
      return;
    }
    const requestId = ++localBootstrapRequestRef.current;
    setLocalBootstrapStatus("loading");
    void bootstrapLocalWorkbench()
      .then(({ workspaces: localWorkspaces }) => {
        if (requestId !== localBootstrapRequestRef.current) return;
        qc.setQueryData(workspaceKeys.list(), localWorkspaces);
        setLocalBootstrapStatus("ready");
      })
      .catch((error) => {
        if (requestId !== localBootstrapRequestRef.current) return;
        console.error("Failed to bootstrap local workbench", error);
        setLocalBootstrapStatus("failed");
      });
  }, [
    localBootstrapGeneration,
    localBootstrapStatus,
    qc,
  ]);

  // Local mode has no account token. Point the Desktop-owned CLI profile at
  // the loopback API and start the daemon directly.
  useEffect(() => {
    if (!runtimeConfig) return;
    (async () => {
      try {
        await window.daemonAPI.setTargetApiUrl(runtimeConfig.apiUrl);
        await window.daemonAPI.autoStart();
      } catch (err) {
        console.error("Failed to start local daemon", err);
      }
    })();
  }, [runtimeConfig]);

  const workspaceListQuery = useQuery({
    ...workspaceListOptions(),
    enabled: true,
    select: selectLocalWorkspaceList,
  });
  const workspaces = workspaceListQuery.data ?? EMPTY_LOCAL_WORKSPACES;
  const workspaceListReady = workspaceListQuery.data !== undefined;
  const workspaceListUnavailable =
    !workspaceListReady && workspaceListQuery.isLoadingError;
  const workspaceListRetrying = workspaceListQuery.isFetching;
  const retryWorkspaceList = workspaceListQuery.refetch;

  // Bridge local daemon IPC status into the runtimes cache so this machine's
  // daemon flips to offline/online without waiting on the server sweeper.
  const activeWorkspaceSlug = useTabStore((s) => s.activeWorkspaceSlug);
  const activeWsId = activeWorkspaceSlug
    ? workspaces.find((w) => w.slug === activeWorkspaceSlug)?.id
    : undefined;
  useDaemonIPCBridge(activeWsId);

  // Validate persisted tab state against the local workspace list,
  // and pick an active workspace if none is set. Runs in useLayoutEffect
  // (synchronously after render, before paint) rather than the render
  // phase — the original render-phase pattern triggered React's
  // "Cannot update a component while rendering a different component"
  // warning because `switchWorkspace` is a Zustand setState that the
  // TabBar is subscribed to. useLayoutEffect flushes both renders before
  // the user sees anything, so there's no visible flicker.
  //
  // Gate on authoritative data: pending and initial errors expose no data,
  // while a failed background refetch retains the last successful list.
  useLayoutEffect(() => {
    if (!workspaceListReady) return;
    const validSlugs = new Set(workspaces.map((w) => w.slug));
    useTabStore.getState().validateWorkspaceSlugs(validSlugs);
    const { activeWorkspaceSlug, switchWorkspace } = useTabStore.getState();
    if (!activeWorkspaceSlug && workspaces.length > 0) {
      switchWorkspace(workspaces[0].slug);
    }
  }, [workspaces, workspaceListReady]);

  if (localBootstrapStatus === "failed") {
    return (
      <LocalWorkbenchRecoveryPage
        onRetry={() => {
          setLocalBootstrapStatus("idle");
          setLocalBootstrapGeneration((generation) => generation + 1);
        }}
      />
    );
  }
  if (localBootstrapStatus !== "ready") {
    return (
      <div className="flex h-screen items-center justify-center">
        <MulticaIcon className="size-6 animate-pulse" />
      </div>
    );
  }

  if (workspaceListUnavailable) {
    return (
      <LocalWorkbenchRecoveryPage
        isRetrying={workspaceListRetrying}
        onRetry={() => {
          void retryWorkspaceList();
        }}
      />
    );
  }

  return <DesktopShell />;
}

function BlockingRuntimeConfigError({ message }: { message: string }) {
  return (
    <div className="flex h-screen items-center justify-center bg-background p-8 text-foreground">
      <div className="max-w-xl rounded-lg border bg-card p-6 shadow-sm">
        <h1 className="text-title font-semibold">
          Local workbench initialization error
        </h1>
        <p className="mt-3 text-body text-muted-foreground">
          Multica Desktop could not initialize its local services. Check the
          error below and restart the app.
        </p>
        <pre className="mt-4 whitespace-pre-wrap rounded-md bg-muted p-3 text-caption text-muted-foreground">
          {message}
        </pre>
      </div>
    </div>
  );
}

export default function App() {
  const { version, os } = window.desktopAPI.appInfo;
  const systemLocale = window.desktopAPI.systemLocale;
  const runtimeConfigResult = window.desktopAPI.runtimeConfig;
  const localRuntimeError =
    runtimeConfigResult.ok &&
    ![
      runtimeConfigResult.config.apiUrl,
      runtimeConfigResult.config.wsUrl,
      runtimeConfigResult.config.appUrl,
    ].every(isLoopbackApiUrl)
      ? "Multica Desktop local mode only accepts loopback runtime URLs."
      : null;
  // The fallback keeps renderer HMR safe while a main/preload rebuild is
  // restarting Electron; packaged builds always expose windowContext.
  const windowContext =
    window.desktopAPI.windowContext ?? { kind: "main" as const };
  useCmdWCloseTab();
  // Mounted at the App root for the same reason as Cmd+W: the chord has to
  // work in every renderer state, not only inside the tab shell.
  useOpenSettingsShortcut();

  // Flush a freeze/crash breadcrumb the main process parked from a previous
  // session. A true hang or process death can't report itself when it happens
  // (the renderer is blocked or gone), so the main process persists it and we
  // emit it here on the next boot. The in-thread, recoverable freeze tier is
  // handled separately by the shared watchdog in CoreProvider.
  useEffect(
    () =>
      flushFreezeBreadcrumb({
        getLastFreeze: () => window.desktopAPI.getLastFreeze(),
        ackFreeze: (ts) => window.desktopAPI.ackFreeze(ts),
        capture: captureEvent,
      }),
    [],
  );

  // Stable identity reference so downstream effects (WS reconnect) don't
  // tear down on every parent render.
  const identity = useMemo(
    () => ({ platform: "desktop", version, os }),
    [version, os],
  );
  // Locale resolution happens once at app boot. Switching language goes
  // through window.location.reload() to avoid hydration mismatch.
  const localeAdapter = useMemo(
    () => createDesktopLocaleAdapter(systemLocale),
    [systemLocale],
  );
  const locale = useMemo(() => pickLocale(localeAdapter), [localeAdapter]);
  const resources = useMemo(
    () => ({ [locale]: RESOURCES[locale] }),
    [locale],
  );

  // Keep <html lang> in sync with the resolved locale (index.html hardcodes
  // "en"). Drives the lang-scoped Japanese CJK font override and a11y.
  // useLayoutEffect (not useEffect) so lang is committed before the first
  // paint — otherwise Japanese users would see one frame of Kanji rendered
  // with the Chinese-first fallback stack before the override kicks in.
  useLayoutEffect(() => {
    document.documentElement.lang = HTML_LANG[locale];
  }, [locale]);

  // React to OS-level language changes detected by main on focus regain.
  // Only act when the user is following the system signal (no explicit
  // Settings choice) — otherwise their preference wins. Cross-device sync
  // for the explicit-choice case is handled inside CoreProvider.
  useEffect(() => {
    return window.desktopAPI.onSystemLocaleChanged((nextSystemLocale) => {
      if (localeAdapter.getUserChoice()) return;
      const next = pickLocale({
        ...localeAdapter,
        getSystemPreferences: () =>
          nextSystemLocale ? [nextSystemLocale] : [],
      });
      if (next === locale) return;
      localeAdapter.persist(next);
      window.location.reload();
    });
  }, [localeAdapter, locale]);

  return (
    <ThemeProvider>
      {runtimeConfigResult.ok && !localRuntimeError ? (
        <CoreProvider
          apiBaseUrl={runtimeConfigResult.config.apiUrl}
          wsUrl={runtimeConfigResult.config.wsUrl}
          localMode
          identity={identity}
          locale={locale}
          resources={resources}
          localeAdapter={localeAdapter}
        >
          {windowContext.kind === "main" && <DiagnosticRouteReporter />}
          {windowContext.kind === "issue" ? (
            <IssueWindowContent />
          ) : (
            <AppContent />
          )}
        </CoreProvider>
      ) : (
        <BlockingRuntimeConfigError
          message={
            localRuntimeError ||
            (runtimeConfigResult.ok
              ? "Invalid local runtime configuration"
              : runtimeConfigResult.error.message)
          }
        />
      )}
      <Toaster />
      {windowContext.kind === "main" && <UpdateNotification />}
    </ThemeProvider>
  );
}
