import { onlineManager } from "@tanstack/react-query";
import { toast } from "sonner";

import { t } from "@/i18n";

/**
 * Setting writes are sent even offline, so they fail at once: replayed on
 * reconnect, a write would replace whatever another device saved at its key
 * in the meantime.
 */
export const SETTING_WRITE_NETWORK_MODE = "always";

/**
 * Tells the user an offline preference change wasn't saved. For callers that
 * show the change at once and report no save error of their own; failures
 * while online stay with the caller's existing handling.
 *
 * Returns whether the failure happened offline. Only then is it certain the
 * write never reached the server and no settings re-read ran first, so only
 * then may a caller put back the value it showed before the change.
 */
export function reportOfflineSettingFailure(): boolean {
  if (onlineManager.isOnline()) return false;
  // One toast for a burst of offline changes, not one per click.
  toast.error(t("web.settings.offlineChangeNotSaved"), { id: "setting-write-offline" });
  return true;
}

/**
 * The reads a failed write refreshes wait for reconnect while offline, and an
 * in-flight one can pause on its retry, so a caller awaiting them would show
 * the failure only after reconnect. Offline, refresh without waiting.
 */
export function awaitUnlessOffline(reconcile: Promise<void>): Promise<void> | undefined {
  return onlineManager.isOnline() ? reconcile : undefined;
}
