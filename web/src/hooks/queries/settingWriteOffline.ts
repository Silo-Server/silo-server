import { useCallback, useRef } from "react";
import { onlineManager } from "@tanstack/react-query";
import { toast } from "sonner";

import { t } from "@/i18n";
import { appearanceCache, type StorageKey } from "@/utils/storage";

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

interface ShownChange<T> {
  /** Identifies the preference; a newer change to it supersedes an older one. */
  key: string;
  /** The browser cache entry that warms the next load with this preference. */
  cacheKey: StorageKey;
  value: T;
  /** What is shown now, put back if the change fails offline. */
  previous: T;
  show: (value: T) => void;
  save: (value: T) => Promise<unknown>;
}

/**
 * For appearance preferences applied before they are saved: shows the change
 * and caches it, then saves it. After an offline failure it puts back that
 * preference's previous value and cache entry, unless a newer change to the
 * same preference has replaced it. Their sync effects re-run only when the
 * stored value changes, which a failed save never does.
 */
export function useShowThenSave(cacheOwner: string | null) {
  const latest = useRef(new Map<string, ShownChange<unknown>>());
  return useCallback(
    <T>(change: ShownChange<T>) => {
      const { key, cacheKey, value, previous, show, save } = change;
      const cached = appearanceCache.get(cacheKey, cacheOwner);
      latest.current.set(key, change as ShownChange<unknown>);
      show(value);
      appearanceCache.set(cacheKey, String(value), cacheOwner);
      save(value).catch(() => {
        if (!reportOfflineSettingFailure() || latest.current.get(key) !== change) return;
        show(previous);
        if (cached === null) appearanceCache.remove(cacheKey, cacheOwner);
        else appearanceCache.set(cacheKey, cached, cacheOwner);
      });
    },
    [cacheOwner],
  );
}
