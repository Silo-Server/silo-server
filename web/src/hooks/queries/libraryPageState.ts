import { useCallback, useMemo } from "react";
import {
  useDeviceSetting,
  useEffectiveSettings,
  useSetDeviceSetting,
} from "@/hooks/queries/settings";
import { storage } from "@/utils/storage";

export const LIBRARY_PAGE_STATE_SETTING_KEY = "ui.library_page_state";
export const REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY = "ui.remember_library_page_state";

export interface LibraryPageStatePreference {
  version: 1;
  libraries: Record<string, { search: string }>;
}

export function parseLibraryPageStatePreference(
  raw: string | null | undefined,
): LibraryPageStatePreference {
  if (!raw) {
    return createEmptyLibraryPageStatePreference();
  }
  try {
    const value = JSON.parse(raw) as unknown;
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
  } catch {
    return createEmptyLibraryPageStatePreference();
  }
}

export function serializeLibraryPageStatePreference(
  preference: LibraryPageStatePreference,
): string {
  return JSON.stringify(preference);
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
  const profileId = storage.get(storage.KEYS.PROFILE_ID);
  const setting = useDeviceSetting(profileId, LIBRARY_PAGE_STATE_SETTING_KEY);
  const rememberSetting = useEffectiveSettings(profileId, [
    REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY,
  ]);
  const mutation = useSetDeviceSetting();
  const { mutate } = mutation;
  const preference = useMemo(() => parseLibraryPageStatePreference(setting.data), [setting.data]);
  const rememberEnabled =
    rememberSetting.data?.[REMEMBER_LIBRARY_PAGE_STATE_SETTING_KEY]?.effective_value !== "false";
  const saveLibrarySearch = useCallback(
    (libraryId: number, search: string) => {
      const nextPreference = updateLibraryPageStatePreference(preference, libraryId, search);
      mutate({
        key: LIBRARY_PAGE_STATE_SETTING_KEY,
        value: serializeLibraryPageStatePreference(nextPreference),
      });
    },
    [mutate, preference],
  );

  return {
    isLoading: setting.isLoading || rememberSetting.isLoading,
    preference,
    rememberEnabled,
    saveLibrarySearch,
  };
}
