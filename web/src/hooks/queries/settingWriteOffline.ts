import { useCallback, useLayoutEffect, useRef } from "react";
import { onlineManager } from "@tanstack/react-query";
import { toast } from "sonner";

import { t } from "@/i18n";
import { appearanceCache, type StorageKey } from "@/utils/storage";

/**
 * A setting write refused unsent because the browser was offline. Its message
 * is the localized offline notice, so pages that show a save error's message
 * show that instead of transport text.
 */
export class SettingWriteOfflineError extends Error {
  constructor() {
    super(t("web.settings.offlineChangeNotSaved"));
    this.name = "SettingWriteOfflineError";
  }
}

/**
 * Options for every setting write. It runs whatever the reported network
 * state, so an offline write is refused at once instead of pausing: queued and
 * replayed on reconnect, it would replace whatever another device saved at its
 * key in the meantime. It is refused before dispatch, which is what makes the
 * failure certain: a sent request can still land after the connection drops,
 * and one held for a session refresh could go out after it returns. A throwing
 * onMutate fails the mutation without running mutationFn.
 */
export const settingWriteOptions = {
  networkMode: "always",
  onMutate: (): undefined => {
    if (!onlineManager.isOnline()) throw new SettingWriteOfflineError();
  },
} as const;

/** Whether a setting write was refused unsent because the browser was offline. */
export function refusedOffline(error: unknown): boolean {
  return error instanceof SettingWriteOfflineError;
}

/**
 * Tells the user a preference change made offline wasn't saved, for callers
 * that show the change at once and report no save error of their own. Other
 * failures stay with the caller's existing handling. Returns whether the
 * write was refused offline, so it is certain nothing was stored.
 */
export function reportOfflineSettingFailure(error: unknown): boolean {
  if (!refusedOffline(error)) return false;
  // One toast for a burst of offline changes, not one per click.
  toast.error(t("web.settings.offlineChangeNotSaved"), { id: "setting-write-offline" });
  return true;
}

interface ShownChange<T> {
  /** Identifies the preference; a newer change to it supersedes an older one. */
  key: string;
  /** The browser cache entry that warms the next load with this preference. */
  cacheKey: StorageKey;
  value: T;
  /** What is shown now, put back if the change is refused offline. */
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
 * For appearance preferences applied before they are saved: shows the change
 * and caches it, then saves it. When the newest change to a preference is
 * refused offline, it puts back the last value known stored, with its cache
 * entry: the one from before the first unsettled change, or a later change
 * that has since landed. Their sync effects re-run only when the stored value
 * changes, which a refused save never does.
 */
export function useShowThenSave(cacheOwner: string | null) {
  const unsettled = useRef(new Map<string, UnsettledPreference>());
  // A restore shows the value through the provider's current state, so it is
  // skipped once another account or profile owns the appearance.
  const currentOwner = useRef(cacheOwner);
  useLayoutEffect(() => {
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
      show(value);
      appearanceCache.set(cacheKey, String(value), cacheOwner);
      save(value).then(
        () => {
          // Even an older change that lands moves the value a failure restores.
          pending.stored = { value, cached: String(value) };
          if (pending.latest === token) unsettled.current.delete(entryKey);
        },
        (error: unknown) => {
          // A newer change to this preference settles it instead.
          if (pending.latest !== token) return;
          unsettled.current.delete(entryKey);
          if (!reportOfflineSettingFailure(error)) return;
          if (currentOwner.current === cacheOwner) show(pending.stored.value as T);
          // The cache is the original owner's either way, read on its next load.
          if (pending.stored.cached === null) appearanceCache.remove(cacheKey, cacheOwner);
          else appearanceCache.set(cacheKey, pending.stored.cached, cacheOwner);
        },
      );
    },
    [cacheOwner],
  );
}
