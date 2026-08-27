import { useAuthStore } from "@multica/core/auth";
import { toast } from "sonner";
import { LOCAL_DAEMON_TOKEN } from "./daemon-login-sync";

/**
 * Re-establish the local daemon's credentials after it failed to authenticate
 * (daemon state "auth_expired", surfaced by daemon-manager's token probe — see
 * #3512).
 *
 * The local desktop owns a stable PAT-shaped compatibility token. Recovery
 * rewrites that token into the checkout-owned profile and restarts the daemon;
 * it never falls back to a hosted login flow.
 *
 * Every failure keeps the deterministic local operator signed in and surfaces a
 * retryable error. There is no valid session-invalid-to-login transition in the
 * fixed local identity model.
 */
export async function reauthenticateDaemon(): Promise<void> {
  const user = useAuthStore.getState().user;
  if (!user) {
    toast.error("Couldn't reconnect the daemon", {
      description: "The local operator has not finished loading. Please try again.",
    });
    return;
  }

  try {
    const result = await window.daemonAPI.reauthenticate(
      LOCAL_DAEMON_TOKEN,
      user.id,
    );
    if (result.ok) return; // daemon restarting; status flips via onStatusChange
    // The local desktop has no hosted login fallback. Keep the deterministic
    // operator intact for every recovery failure and let the user retry.
    toast.error("Couldn't reconnect the daemon", {
      description:
        result.reason === "transient"
          ? result.message
          : "The local daemon rejected its compatibility token. Please try again.",
    });
  } catch (err) {
    // An unexpected IPC error is not an auth failure — never log out on it.
    toast.error("Couldn't reconnect the daemon", {
      description: err instanceof Error ? err.message : "Please try again.",
    });
  }
}
