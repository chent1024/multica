"use client";

import { useEffect, useMemo } from "react";
import { ApiClient } from "../api/client";
import { installFreezeWatchdog } from "../diagnostics/freeze-watchdog";
import { setApiInstance, setSchemaLogger } from "../api";
import { createAuthStore, registerAuthStore } from "../auth";
import { createChatStore, registerChatStore } from "../chat";
import {
  I18nProvider,
  LocaleAdapterProvider,
  UserLocaleSync,
} from "../i18n/react";
import { WSProvider } from "../realtime";
import { QueryProvider } from "../provider";
import { createLogger } from "../logger";
import { defaultStorage } from "./storage";
import { AuthInitializer } from "./auth-initializer";
import type { CoreProviderProps, ClientIdentity } from "./types";
import type { StorageAdapter } from "../types/storage";
import type { User } from "../types";
import { ClientUsageReporter } from "../client-usage";
import {
  configureShortcutPlatform,
  configureShortcutRuntime,
} from "../shortcuts/platform";

// Module-level singletons — created once at first render, never recreated.
// Vite HMR preserves module-level state, so these survive hot reloads.
let initialized = false;
let authStore: ReturnType<typeof createAuthStore>;
let chatStore: ReturnType<typeof createChatStore>;
const LOCAL_ACTOR_TIMESTAMP = "2026-01-01T00:00:00Z";
const LOCAL_ACTOR: User = {
  id: "00000000-0000-4000-8000-000000000001",
  name: "Local",
  email: "",
  avatar_url: null,
  onboarded_at: LOCAL_ACTOR_TIMESTAMP,
  onboarding_questionnaire: {},
  starter_content_state: "imported",
  language: null,
  profile_description: "",
  timezone: null,
  created_at: LOCAL_ACTOR_TIMESTAMP,
  updated_at: LOCAL_ACTOR_TIMESTAMP,
};
function initCore(
  apiBaseUrl: string,
  storage: StorageAdapter,
  onLogin?: () => void,
  onLogout?: () => void,
  cookieAuth?: boolean,
  localMode?: boolean,
  identity?: ClientIdentity,
) {
  if (initialized) return;

  configureShortcutPlatform(
    identity?.os === "macos" ||
      identity?.os === "windows" ||
      identity?.os === "linux" ||
      identity?.os === "unknown"
      ? identity.os
      : null,
  );
  // Authoritative override; before this runs (module-eval store hydration)
  // detectShortcutRuntime() reads the preload globals and already agrees.
  configureShortcutRuntime(
    identity?.platform === "desktop" ? "desktop" : null,
  );

  const api = new ApiClient(apiBaseUrl, {
    logger: createLogger("api"),
    localMode,
    onUnauthorized: () => {
      storage.removeItem("multica_token");
    },
    identity,
  });
  setApiInstance(api);
  setSchemaLogger(createLogger("api-schema"));

  // In token mode, hydrate token from storage.
  if (!cookieAuth && !localMode) {
    const token = storage.getItem("multica_token");
    if (token) api.setToken(token);
  }
  // Workspace identity is URL-driven: the [workspaceSlug] layout resolves
  // the slug and calls setCurrentWorkspace(slug, wsId) on mount. The api
  // client reads the slug from that singleton for the X-Workspace-Slug
  // header. No boot-time hydration from storage is required.

  authStore = createAuthStore({ api, storage, onLogin, onLogout, cookieAuth });
  if (localMode) {
    // Shared views still use the historical actor-shaped authorship fields.
    // This is an in-memory system actor, not an account or authenticated session.
    authStore.setState({ user: LOCAL_ACTOR, isLoading: false, status: "local" });
  }
  registerAuthStore(authStore);

  chatStore = createChatStore({ storage });
  registerChatStore(chatStore);

  initialized = true;
}

export function CoreProvider({
  children,
  apiBaseUrl = "",
  wsUrl = "ws://localhost:8080/ws",
  storage = defaultStorage,
  cookieAuth,
  localMode,
  onLogin,
  onLogout,
  identity,
  locale,
  resources,
  localeAdapter,
}: CoreProviderProps) {
  // Initialize singletons on first render only. Dependencies are read-once:
  // apiBaseUrl, storage, and callbacks are set at app boot and never change at runtime.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useMemo(() => initCore(apiBaseUrl, storage, onLogin, onLogout, cookieAuth, localMode, identity), []);

  // Client-only freeze watchdog — shared by web and desktop. No-op on the
  // server and idempotent, so mounting it here covers both apps in one place.
  useEffect(() => {
    installFreezeWatchdog();
  }, []);

  // I18nProvider wraps everything else: server and client must use the same
  // (locale, resources) to avoid hydration mismatch. Language switching goes
  // through window.location.reload(), never client-side changeLanguage.
  const tree = (
    <QueryProvider>
      <AuthInitializer
        onLogin={onLogin}
        onLogout={onLogout}
        storage={storage}
        cookieAuth={cookieAuth}
        localMode={localMode}
        identity={identity}
      >
        {/* Desktop's reporter owns both activity and runtime state so it must
            be the only writer for that installation. */}
        {identity?.platform !== "desktop" && (
          <ClientUsageReporter storage={storage} identity={identity} />
        )}
        <WSProvider
          wsUrl={wsUrl}
          authStore={authStore}
          storage={storage}
          cookieAuth={cookieAuth}
          localMode={localMode}
          identity={identity}
        >
          {children}
        </WSProvider>
      </AuthInitializer>
    </QueryProvider>
  );

  // UserLocaleSync requires a LocaleAdapter to persist; only mount it when
  // the host app provides one (web layout + desktop App both do).
  const withAdapter = localeAdapter ? (
    <LocaleAdapterProvider adapter={localeAdapter}>
      <UserLocaleSync />
      {tree}
    </LocaleAdapterProvider>
  ) : (
    tree
  );

  return (
    <I18nProvider locale={locale} resources={resources}>
      {withAdapter}
    </I18nProvider>
  );
}
