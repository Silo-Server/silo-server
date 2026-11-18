import { useCallback, useEffect, useRef } from "react";
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

interface UnsettledPreference {
  /** The newest change; only its failure may restore. */
  latest: object;
  /** The last value known stored, and its cache entry, to restore. */
  stored: { value: unknown; cached: string | null };
}

/**
 * Whether a write about to be sent will fail without reaching the server.
 * Read it before sending: a write sent online can still land when the
 * connection drops before its answer, so being offline when it fails proves
 * nothing.
 */
export function sendingOffline(): boolean {
  return !onlineManager.isOnline();
}

/**
 * For appearance preferences applied before they are saved: shows the change
 * and caches it, then saves it. When the newest change to a preference was
 * sent offline and fails, it puts back the last value known stored, with its
 * cache entry: the one from before the first unsettled change, or a later
 * change that has since landed. Their sync effects re-run only when the stored
 * value changes, which a failed save never does.
 */
export function useShowThenSave(cacheOwner: string | null) {
  const unsettled = useRef(new Map<string, UnsettledPreference>());
  // A restore shows the value through the provider's current state, so it is
  // skipped once another account or profile owns the appearance.
  const currentOwner = useRef(cacheOwner);
  useEffect(() => {
    currentOwner.current = cacheOwner;
  }, [cacheOwner]);
  return useCallback(
    <T>({ key, cacheKey, value, previous, show, save }: ShownChange<T>) => {
      const token = {};
      // Per owner: another profile's unsettled change says nothing about this one's.
      const entryKey = `${cacheOwner ?? ""}\u0000${key}`;
      const pending = unsettled.current.get(entryKey) ?? {
        latest: token,
        stored: { value: previous, cached: appearanceCache.get(cacheKey, cacheOwner) },
      };
      pending.latest = token;
      unsettled.current.set(entryKey, pending);
      const offline = sendingOffline();
      show(value);
      appearanceCache.set(cacheKey, String(value), cacheOwner);
      save(value).then(
        () => {
          // Even an older change that lands moves the value a failure restores.
          pending.stored = { value, cached: String(value) };
          if (pending.latest === token) unsettled.current.delete(entryKey);
        },
        () => {
          // A newer change to this preference settles it instead.
          if (pending.latest !== token) return;
          unsettled.current.delete(entryKey);
          if (!offline || !reportOfflineSettingFailure()) return;
          if (currentOwner.current !== cacheOwner) return;
          show(pending.stored.value as T);
          if (pending.stored.cached === null) appearanceCache.remove(cacheKey, cacheOwner);
          else appearanceCache.set(cacheKey, pending.stored.cached, cacheOwner);
        },
      );
    },
    [cacheOwner],
  );
}
