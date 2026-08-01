import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { UserLibrary } from "@/api/types";
import { useAuth } from "@/hooks/useAuth";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { libraryKeys } from "./keys";
import { useEffectiveSettings } from "./settingValues";

const LIBRARY_PREF_KEYS = [
  SETTING_KEYS.UI_DISABLED_LIBRARY_IDS,
  SETTING_KEYS.UI_LIBRARY_ORDER,
] as const;

/**
 * Drops non-integers, duplicates, and anything below 1 — the same
 * normalization library-id-list.json enforces server-side, applied before a
 * write so a value the client sends always validates.
 */
export function normalizeLibraryIDs(ids: number[]) {
  return [...new Set(ids.filter((id) => Number.isInteger(id) && id > 0))];
}

/**
 * Accepts the canonical array value, the legacy JSON-string encoding, or
 * null/undefined (the contract default), and always lands on a normalized id
 * list. Backs both ui.disabled_library_ids and ui.library_order.
 */
export function parseLibraryIDList(value: unknown): number[] {
  if (value == null) return [];
  let parsed: unknown = value;
  if (typeof value === "string") {
    if (!value) return [];
    try {
      parsed = JSON.parse(value);
    } catch {
      return [];
    }
  }
  if (!Array.isArray(parsed)) return [];
  return normalizeLibraryIDs(
    parsed.map((entry) => (typeof entry === "number" ? entry : Number.NaN)),
  );
}

export function applyLibraryOrder(libraries: UserLibrary[], orderIDs: number[]): UserLibrary[] {
  if (orderIDs.length === 0) return libraries;
  const pos = new Map(orderIDs.map((id, i) => [id, i]));
  const ordered: UserLibrary[] = [];
  const tail: UserLibrary[] = [];
  for (const lib of libraries) {
    if (pos.has(lib.id)) {
      ordered.push(lib);
    } else {
      tail.push(lib);
    }
  }
  ordered.sort((a, b) => (pos.get(a.id) ?? 0) - (pos.get(b.id) ?? 0));
  return [...ordered, ...tail];
}

export function filterVisibleLibraries(libraries: UserLibrary[], disabledLibraryIDs: number[]) {
  if (disabledLibraryIDs.length === 0) return libraries;
  const disabled = new Set(disabledLibraryIDs);
  return libraries.filter((library) => !disabled.has(library.id));
}

export function useAvailableUserLibraries() {
  const { profile } = useAuth();

  return useQuery({
    queryKey: libraryKeys.user(profile?.id),
    queryFn: () => api<UserLibrary[]>("/user/libraries"),
    staleTime: 5 * 60 * 1000,
  });
}

/**
 * The profile's own hide/order preferences, resolved through the canonical
 * settings API in one batched read. Exported for the settings screen that
 * edits them; most callers want useUserLibraries, which applies them.
 */
export function useLibraryDisplayPreferences() {
  const { profile } = useAuth();
  const query = useEffectiveSettings({ keys: LIBRARY_PREF_KEYS, enabled: Boolean(profile) });
  const disabledValue = query.data?.[SETTING_KEYS.UI_DISABLED_LIBRARY_IDS]?.value;
  const orderValue = query.data?.[SETTING_KEYS.UI_LIBRARY_ORDER]?.value;
  // Memoized so effects keyed on these lists fire on saved-value changes, not
  // on every render.
  const disabledLibraryIDs = useMemo(() => parseLibraryIDList(disabledValue), [disabledValue]);
  const libraryOrder = useMemo(() => parseLibraryIDList(orderValue), [orderValue]);
  return {
    ...query,
    disabledLibraryIDs,
    libraryOrder,
    // A profile-less session has no preferences to wait for.
    isLoading: Boolean(profile) && query.isLoading,
  };
}

export function useUserLibraries() {
  const librariesQuery = useAvailableUserLibraries();
  const prefs = useLibraryDisplayPreferences();

  let data = librariesQuery.data;
  if (data != null && !prefs.isLoading) {
    data = filterVisibleLibraries(data, prefs.disabledLibraryIDs);
    data = applyLibraryOrder(data, prefs.libraryOrder);
  }

  return {
    ...librariesQuery,
    data,
    isLoading: librariesQuery.isLoading || prefs.isLoading,
    isFetching: librariesQuery.isFetching || prefs.isFetching,
    error: librariesQuery.error ?? prefs.error,
  };
}
