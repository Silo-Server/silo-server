import { useCallback, useMemo } from "react";
import { useEffectiveSettings, useSetSettingValue } from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { storage } from "@/utils/storage";

/** Both keys are profile+device scoped in the contract. */
const DEVICE_SCOPE: SettingIdentity = { scope: "profile_device" };

const PAGE_STATE_KEYS = [
  SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
  SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE,
] as const;

export interface LibraryPageStatePreference {
  version: 1;
  libraries: Record<string, { search: string }>;
}

/**
 * Accepts the canonical object value, the legacy JSON-string encoding, or
 * null/undefined (the contract default), and always lands on a usable
 * preference.
 */
export function parseLibraryPageStatePreference(raw: unknown): LibraryPageStatePreference {
  if (raw == null) {
    return createEmptyLibraryPageStatePreference();
  }
  let value: unknown = raw;
  if (typeof raw === "string") {
    if (!raw) {
      return createEmptyLibraryPageStatePreference();
    }
    try {
      value = JSON.parse(raw);
    } catch {
      return createEmptyLibraryPageStatePreference();
    }
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return createEmptyLibraryPageStatePreference();
  }
  const maybePreference = value as {
    version?: unknown;
    libraries?: unknown;
  };
  if (maybePreference.version !== 1 || !maybePreference.libraries) {
    return createEmptyLibraryPageStatePreference();
  }
  if (typeof maybePreference.libraries !== "object" || Array.isArray(maybePreference.libraries)) {
    return createEmptyLibraryPageStatePreference();
  }

  const libraries: LibraryPageStatePreference["libraries"] = {};
  Object.entries(maybePreference.libraries).forEach(([libraryId, entry]) => {
    if (!/^\d+$/.test(libraryId) || !entry || typeof entry !== "object" || Array.isArray(entry)) {
      return;
    }
    const search = (entry as { search?: unknown }).search;
    if (typeof search !== "string") {
      return;
    }
    libraries[libraryId] = { search };
  });

  return { version: 1, libraries };
}

export function updateLibraryPageStatePreference(
  preference: LibraryPageStatePreference,
  libraryId: number,
  search: string,
): LibraryPageStatePreference {
  return {
    version: 1,
    libraries: {
      ...preference.libraries,
      [String(libraryId)]: { search },
    },
  };
}

function createEmptyLibraryPageStatePreference(): LibraryPageStatePreference {
  return { version: 1, libraries: {} };
}

export function useLibraryPageStatePreference() {
  // The effective endpoint requires a profile header; before one is chosen
  // there is no saved state to restore and nowhere to save it.
  const enabled = Boolean(storage.get(storage.KEYS.PROFILE_ID));
  const { data, isLoading } = useEffectiveSettings({ keys: PAGE_STATE_KEYS, enabled });
  const mutation = useSetSettingValue();
  const { mutate } = mutation;

  const stateValue = data?.[SETTING_KEYS.UI_LIBRARY_PAGE_STATE]?.value;
  const preference = useMemo(() => parseLibraryPageStatePreference(stateValue), [stateValue]);
  // The contract default is true; anything but an explicit false keeps the
  // feature on, matching the legacy `!== "false"` reading.
  const rememberEnabled = data?.[SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE]?.value !== false;
  const saveLibrarySearch = useCallback(
    (libraryId: number, search: string) => {
      const nextPreference = updateLibraryPageStatePreference(preference, libraryId, search);
      mutate({
        key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
        value: nextPreference,
        identity: DEVICE_SCOPE,
      });
    },
    [mutate, preference],
  );

  return {
    isLoading: enabled && isLoading,
    preference,
    rememberEnabled,
    saveLibrarySearch,
  };
}
