import { useCallback, useLayoutEffect, useRef } from "react";
import { onlineManager } from "@tanstack/react-query";
import { toast } from "sonner";

import { readableErrorText, V2ProblemError } from "@/api/v2/request";
import { t } from "@/i18n";
import { appearanceCache, type StorageKey } from "@/utils/storage";

import { isDefinitiveSettingMutationRejection } from "./settingValues";

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
 * Tells the user a preference change made offline wasn't saved, once for a
 * burst of them. Other failures stay with the caller's own handling. Returns
 * whether the write was refused offline, so it is certain nothing was stored.
 */
export function reportOfflineSettingFailure(error: unknown): boolean {
  if (!refusedOffline(error)) return false;
  // One toast for a burst of offline changes, not one per click.
  toast.error(t("web.settings.offlineChangeNotSaved"), { id: "setting-write-offline" });
  return true;
}

/**
 * The error toast for a setting write that reports its own failures: the
 * offline notice once for a burst of offline changes, and otherwise the
 * error's own message, or `fallback` when that is developer English.
 */
export function toastSettingWriteError(error: unknown, fallback: string): void {
  if (!reportOfflineSettingFailure(error)) toast.error(readableErrorText(error, fallback));
}

/**
 * Tells the user a preference change wasn't saved when that is certain: it was
 * refused offline, or the server rejected it (a 4xx other than 408). A
 * rejection triggers no settings re-read, so nothing else corrects what the
 * caller showed. Other failures may have saved, and that re-read decides.
 * Returns whether the change certainly wasn't saved.
 */
export function reportUnsavedSettingChange(error: unknown): boolean {
  if (reportOfflineSettingFailure(error)) return true;
  if (!(error instanceof V2ProblemError) || !isDefinitiveSettingMutationRejection(error)) {
    return false;
  }
  // The problem's own message is already in the interface language.
  toast.error(error.message, { id: "setting-write-rejected" });
  return true;
}

interface ShownChange<T> {
  /** Identifies the preference; a newer change to it supersedes an older one. */
  key: string;
  /** The browser cache entry that warms the next load with this preference, if any. */
  cacheKey?: StorageKey;
  value: T;
  /** What is shown now, put back if the change certainly wasn't saved. */
  previous: T;
  show: (value: T) => void;
  save: (value: T) => Promise<unknown>;
  /**
   * Reports a failed save and returns whether it certainly stored nothing.
   * Defaults to reportUnsavedSettingChange.
   */
  unsaved?: (error: unknown) => boolean;
}

interface UnsettledPreference {
  /** The newest change; only its failure may restore. */
  latest: object;
  /** Changes to this preference whose saves haven't settled. */
  saving: number;
  /** Numbers the changes in the order they were made. */
  made: number;
  /** The last value known stored, the change that stored it, and its cache entry. */
  stored: { value: unknown; change: number; cached: string | null };
  /** Whether the newest change failed and `stored` is shown in its place. */
  restored: boolean;
}

/**
 * For preferences applied before they are saved: shows the change (and caches
 * it, given a cache key), then saves it. When the newest change to a
 * preference certainly wasn't saved (refused offline, or rejected by the
 * server), it puts back the last value known stored, with its cache entry:
 * the newest change known to have landed, or else the value from before the
 * first unsettled change. An earlier change that lands after that, with
 * nothing picked since, is then shown. Callers' sync effects re-run only when
 * the stored value changes, which a failed save never does.
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
    <T>({
      key,
      cacheKey,
      value,
      previous,
      show,
      save,
      unsaved = reportUnsavedSettingChange,
    }: ShownChange<T>) => {
      const token = {};
      // Per owner: another profile's unsettled change says nothing about this one's.
      const entryKey = `${cacheOwner ?? ""}\u0000${key}`;
      // Kept until every save in it settles, so one that lands late still
      // counts after a newer change has failed or saved.
      const pending: UnsettledPreference = unsettled.current.get(entryKey) ?? {
        latest: token,
        saving: 0,
        made: 0,
        stored: {
          value: previous,
          change: 0,
          cached: cacheKey ? appearanceCache.get(cacheKey, cacheOwner) : null,
        },
        restored: false,
      };
      const change = ++pending.made;
      pending.latest = token;
      pending.saving++;
      pending.restored = false;
      unsettled.current.set(entryKey, pending);
      const settle = () => {
        if (--pending.saving === 0) unsettled.current.delete(entryKey);
      };
      const showStored = () => {
        // Once another owner holds the appearance, leave it alone: writing the
        // cache would also make this owner the signed-out fallback. This
        // owner's namespace is refreshed from the server on its next load.
        if (currentOwner.current !== cacheOwner) return;
        show(pending.stored.value as T);
        if (!cacheKey) return;
        if (pending.stored.cached === null) appearanceCache.remove(cacheKey, cacheOwner);
        else appearanceCache.set(cacheKey, pending.stored.cached, cacheOwner);
      };

      show(value);
      if (cacheKey) appearanceCache.set(cacheKey, String(value), cacheOwner);
      save(value).then(
        () => {
          settle();
          // A change that saved after a newer one doesn't replace it.
          if (change < pending.stored.change) return;
          pending.stored = { value, change, cached: String(value) };
          // It landed after the newest change failed and put back an older
          // value, with nothing picked since: show what is stored now.
          if (pending.restored) showStored();
        },
        (error: unknown) => {
          settle();
          // A newer change to this preference settles it instead.
          if (pending.latest !== token || !unsaved(error)) return;
          pending.restored = true;
          showStored();
        },
      );
    },
    [cacheOwner],
  );
}
