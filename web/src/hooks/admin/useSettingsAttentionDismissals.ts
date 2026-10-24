import { useCallback, useEffect, useMemo, useRef, useSyncExternalStore } from "react";

import { useAuth } from "@/hooks/useAuth";
import { useSettingsAttentionState, type SettingsHealthItem } from "./useSettingsHealth";

const STORAGE_KEY_PREFIX = "silo.admin-dashboard.dismissed-attention.v1";

/** Where this account's dismissals live in localStorage. */
export function settingsAttentionDismissalsKey(userId: number): string {
  return `${STORAGE_KEY_PREFIX}.u${userId}`;
}

/**
 * Whether the admin may hide an item from the Dashboard. Only setup the admin
 * has not done ("Email · Not set up") qualifies: a server can deliberately run
 * without mail or a public URL. A broken service or a restart a saved setting
 * is waiting on stays until it is fixed.
 */
export function isSettingsAttentionDismissible(item: SettingsHealthItem): boolean {
  return item.state === "off";
}

// localStorage only notifies other tabs, so writes from this tab notify the
// subscribers here themselves.
const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

function readRaw(key: string | null): string | null {
  if (key === null) return null;
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function parseIds(raw: string | null): string[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed)
      ? parsed.filter((value): value is string => typeof value === "string")
      : [];
  } catch {
    return [];
  }
}

function writeIds(key: string, ids: readonly string[]) {
  try {
    if (ids.length === 0) {
      window.localStorage.removeItem(key);
    } else {
      window.localStorage.setItem(key, JSON.stringify(ids));
    }
  } catch {
    // Storage full or unavailable: the notice simply stays visible.
  }
  for (const listener of listeners) listener();
}

export interface SettingsAttentionDismissals {
  /** The items the Dashboard notice shows. */
  visible: SettingsHealthItem[];
  /** How many current items the admin has hidden. */
  hiddenCount: number;
  dismiss: (item: SettingsHealthItem) => void;
  restoreAll: () => void;
}

/**
 * The Dashboard's attention items with the ones this admin hid taken out.
 *
 * Dismissals are kept per account in this browser, by item id. Only an item
 * in the dismissible state is hidden, so one that comes back in another state
 * ("Restart pending") shows. A dismissal is forgotten when the Dashboard sees
 * its item resolve, so setup that was done and later undone asks for
 * attention again; setup undone without the Dashboard seeing it done stays
 * hidden, counted in the header chip.
 */
export function useSettingsAttentionDismissals(): SettingsAttentionDismissals {
  const { items, loaded } = useSettingsAttentionState();
  const userId = useAuth().user?.id ?? null;
  const key = userId === null ? null : settingsAttentionDismissalsKey(userId);

  // The raw string is the snapshot: it compares by value, so an unrelated
  // render never sees a "new" list.
  const raw = useSyncExternalStore(
    subscribe,
    () => readRaw(key),
    () => null,
  );
  const dismissed = useMemo(() => new Set(parseIds(raw)), [raw]);

  // The attention item ids this tab last saw for `key`, once every input
  // had loaded.
  const seen = useRef<{ key: string; ids: Set<string> } | null>(null);

  // Only an item that is gone — resolved — drops its dismissal. One passing
  // through another state keeps it: saving an unrelated playback setting turns
  // "Transcoding · Off" into "Restart pending" until the restart, after which
  // it is "Off" again and should stay hidden. After the first full load only
  // an item this tab saw go away counts as resolved: another tab's dismissal
  // arrives through storage, and this tab's cached settings may predate it.
  useEffect(() => {
    if (key === null || !loaded) return;
    const current = new Set(items.map((item) => item.id));
    const previous = seen.current?.key === key ? seen.current.ids : null;
    seen.current = { key, ids: current };
    const resolved = (id: string) => !current.has(id) && (previous === null || previous.has(id));
    const ids = parseIds(readRaw(key));
    const kept = ids.filter((id) => !resolved(id));
    if (kept.length !== ids.length) writeIds(key, kept);
  }, [items, key, loaded]);

  const visible = useMemo(
    () => items.filter((item) => !isSettingsAttentionDismissible(item) || !dismissed.has(item.id)),
    [dismissed, items],
  );

  const dismiss = useCallback(
    (item: SettingsHealthItem) => {
      if (key === null || !isSettingsAttentionDismissible(item)) return;
      const ids = parseIds(readRaw(key));
      if (!ids.includes(item.id)) writeIds(key, [...ids, item.id]);
    },
    [key],
  );

  const restoreAll = useCallback(() => {
    if (key !== null) writeIds(key, []);
  }, [key]);

  return { visible, hiddenCount: items.length - visible.length, dismiss, restoreAll };
}
