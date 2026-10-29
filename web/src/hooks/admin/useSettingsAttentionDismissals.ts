import { useIsFetching } from "@tanstack/react-query";
import { useEffect, useMemo, useRef } from "react";

import { useAdminServerSettings, useUpdateServerSetting } from "@/hooks/queries/admin/settings";
import { adminKeys } from "@/hooks/queries/keys";
import { useSettingsAttentionState, type SettingsHealthItem } from "./useSettingsHealth";

/**
 * The server setting that lists the hidden notices, as a JSON array of item
 * ids. It is server-wide: one admin deciding the server runs without mail
 * stops the notice for every admin, on every device.
 */
export const SETTINGS_ATTENTION_HIDDEN_KEY = "ui.dashboard.hidden_attention";

/**
 * Whether the admin may hide an item from the Dashboard. Only setup the admin
 * has not done ("Email · Not set up") qualifies: a server can deliberately run
 * without mail or a public URL. A broken service or a restart a saved setting
 * is waiting on stays until it is fixed.
 */
export function isSettingsAttentionDismissible(item: SettingsHealthItem): boolean {
  return item.state === "off";
}

function parseIds(raw: string | undefined): string[] {
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

function encodeIds(ids: readonly string[]): string {
  return ids.length === 0 ? "" : JSON.stringify(ids);
}

export interface SettingsAttentionDismissals {
  /** The items the Dashboard notice shows. */
  visible: SettingsHealthItem[];
  /** How many current items an admin has hidden. */
  hiddenCount: number;
  /** A change is saving; another would race it on the settings version. */
  saving: boolean;
  dismiss: (item: SettingsHealthItem) => void;
  restoreAll: () => void;
}

/**
 * The Dashboard's attention items with the ones an admin hid taken out.
 *
 * Dismissals live in a server setting, by item id. Only an item in the
 * dismissible state is hidden, so one that comes back in another state
 * ("Restart pending") shows. A dismissal is forgotten when the Dashboard sees
 * its item resolve, so setup that was done and later undone asks for
 * attention again; setup undone without any admin's Dashboard seeing it done
 * stays hidden, counted in the header chip.
 */
export function useSettingsAttentionDismissals(): SettingsAttentionDismissals {
  const { items, loaded } = useSettingsAttentionState();
  const { data: settings } = useAdminServerSettings();
  const update = useUpdateServerSetting(settings);
  const stored = settings?.[SETTINGS_ATTENTION_HIDDEN_KEY];

  // While a change saves, show it already made; the mutation stays pending
  // until the refetched settings carry it.
  const raw =
    update.isPending && update.variables?.key === SETTINGS_ATTENTION_HIDDEN_KEY
      ? update.variables.value
      : stored;
  const dismissed = useMemo(() => parseIds(raw), [raw]);

  const save = (ids: readonly string[]) =>
    update.mutate({ key: SETTINGS_ATTENTION_HIDDEN_KEY, value: encodeIds(ids) });

  // Only an item that is gone — resolved — drops its dismissal. One passing
  // through another state keeps it: saving an unrelated playback setting turns
  // "Transcoding · Off" into "Restart pending" until the restart, after which
  // it is "Off" again and should stay hidden. Settings and server status
  // refetch separately after a save, and new settings with the old status can
  // read a pending restart as resolved, so nothing is pruned while either is
  // fetching. Each settings read is pruned at most once, so a failed write
  // waits for the next read instead of looping.
  const settingsFetching = useIsFetching({ queryKey: adminKeys.serverSettings() });
  const statusFetching = useIsFetching({ queryKey: adminKeys.serverStatus() });
  const refreshing = settingsFetching > 0 || statusFetching > 0;
  const pruned = useRef<object | undefined>(undefined);
  useEffect(() => {
    if (!loaded || refreshing || update.isPending || !settings || pruned.current === settings) {
      return;
    }
    const ids = parseIds(stored);
    const current = new Set(items.map((item) => item.id));
    const kept = ids.filter((id) => current.has(id));
    if (kept.length === ids.length) return;
    pruned.current = settings;
    update.mutate({ key: SETTINGS_ATTENTION_HIDDEN_KEY, value: encodeIds(kept) });
  }, [items, loaded, refreshing, settings, stored, update]);

  const visible = useMemo(
    () =>
      items.filter((item) => !isSettingsAttentionDismissible(item) || !dismissed.includes(item.id)),
    [dismissed, items],
  );

  return {
    visible,
    hiddenCount: items.length - visible.length,
    saving: update.isPending,
    dismiss: (item) => {
      if (update.isPending || !isSettingsAttentionDismissible(item)) return;
      if (!dismissed.includes(item.id)) save([...dismissed, item.id]);
    },
    restoreAll: () => {
      if (!update.isPending && dismissed.length > 0) save([]);
    },
  };
}
