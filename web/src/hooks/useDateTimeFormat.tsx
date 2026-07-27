import { createContext, useCallback, useContext, useEffect, useSyncExternalStore } from "react";
import type { ReactNode } from "react";

import {
  getDateTimeFormatPreferences,
  parseDateFormatPreference,
  parseTimeFormatPreference,
  setDateTimeFormatPreferences,
  subscribeDateTimeFormatPreferences,
} from "@/lib/datetime";
import type {
  DateFormatPreference,
  DateTimeFormatPreferences,
  TimeFormatPreference,
} from "@/lib/datetime";
import { useSettings, useSetSetting } from "@/hooks/queries/settings";
import { useAppearanceCacheOwner } from "@/hooks/themePreferences";
import { appearanceCache, storage } from "@/utils/storage";

export const DATE_FORMAT_SETTING_KEY = "ui.date_format";
export const TIME_FORMAT_SETTING_KEY = "ui.time_format";

// Seed the shared formatter state from localStorage at module load, before the
// first render, so an app booting straight into a date-heavy page doesn't
// paint in the wrong format while the settings request is in flight.
//
// Auth has not resolved this early, so this reads through the same namespace
// resolution as every other access — the account that last wrote here — rather
// than the bare key. Reading the bare key would seed the formatter with
// whichever account happened to write before namespacing existed.
setDateTimeFormatPreferences({
  dateFormat: parseDateFormatPreference(appearanceCache.get(storage.KEYS.UI_DATE_FORMAT, null)),
  timeFormat: parseTimeFormatPreference(appearanceCache.get(storage.KEYS.UI_TIME_FORMAT, null)),
});

interface DateTimeFormatContextValue {
  dateFormat: DateFormatPreference;
  timeFormat: TimeFormatPreference;
  setDateFormat: (value: DateFormatPreference) => void;
  setTimeFormat: (value: TimeFormatPreference) => void;
}

const DateTimeFormatContext = createContext<DateTimeFormatContextValue | null>(null);

/**
 * Syncs the persisted date/time format settings (user-scoped, mirrored in
 * localStorage like the theme preferences) into the shared formatter state in
 * lib/datetime, and exposes setters for the settings UI.
 */
export function DateTimeFormatProvider({ children }: { children: ReactNode }) {
  // Same owner token as the theme caches, from the same hook, so widening
  // ownership can never move one provider and leave this one behind.
  const cacheOwner = useAppearanceCacheOwner();
  const loadApiSettings = cacheOwner !== null;
  const { data: apiSettings } = useSettings({ enabled: loadApiSettings });
  const settingMutation = useSetSetting();

  const local = useDateTimeFormat();
  // Once the authenticated settings have loaded they are authoritative: a
  // missing key means the user has no preference (auto), not "fall back to
  // whatever this device saw last" — and rollback of a failed save flows back
  // through the query cache. Until then (including when the settings request
  // fails), fall back to this account's own mirrored values rather than the
  // module-level seed, which ran before auth resolved and on a shared browser
  // may hold the account that used this device last.
  const apiLoaded = loadApiSettings && apiSettings !== undefined;
  const dateFormat = apiLoaded
    ? parseDateFormatPreference(apiSettings[DATE_FORMAT_SETTING_KEY])
    : loadApiSettings
      ? parseDateFormatPreference(appearanceCache.get(storage.KEYS.UI_DATE_FORMAT, cacheOwner))
      : local.dateFormat;
  const timeFormat = apiLoaded
    ? parseTimeFormatPreference(apiSettings[TIME_FORMAT_SETTING_KEY])
    : loadApiSettings
      ? parseTimeFormatPreference(appearanceCache.get(storage.KEYS.UI_TIME_FORMAT, cacheOwner))
      : local.timeFormat;

  useEffect(() => {
    setDateTimeFormatPreferences({ dateFormat, timeFormat });
    // Mirror the resolved values into this account's namespace so the next load
    // on this device paints in the right format before the settings request
    // resolves.
    if (apiLoaded) {
      appearanceCache.set(storage.KEYS.UI_DATE_FORMAT, dateFormat, cacheOwner);
      appearanceCache.set(storage.KEYS.UI_TIME_FORMAT, timeFormat, cacheOwner);
    }
  }, [dateFormat, timeFormat, apiLoaded, cacheOwner]);

  const setDateFormat = useCallback(
    (value: DateFormatPreference) => {
      setDateTimeFormatPreferences({ ...getDateTimeFormatPreferences(), dateFormat: value });
      appearanceCache.set(storage.KEYS.UI_DATE_FORMAT, value, cacheOwner);
      settingMutation.mutate({ key: DATE_FORMAT_SETTING_KEY, value });
    },
    [settingMutation, cacheOwner],
  );

  const setTimeFormat = useCallback(
    (value: TimeFormatPreference) => {
      setDateTimeFormatPreferences({ ...getDateTimeFormatPreferences(), timeFormat: value });
      appearanceCache.set(storage.KEYS.UI_TIME_FORMAT, value, cacheOwner);
      settingMutation.mutate({ key: TIME_FORMAT_SETTING_KEY, value });
    },
    [settingMutation, cacheOwner],
  );

  return (
    <DateTimeFormatContext
      value={{
        dateFormat,
        timeFormat,
        setDateFormat,
        setTimeFormat,
      }}
    >
      {children}
    </DateTimeFormatContext>
  );
}

/** Settings-page access to the persisted preferences and their setters. */
export function useDateTimeFormatSettings(): DateTimeFormatContextValue {
  const ctx = useContext(DateTimeFormatContext);
  if (!ctx) {
    throw new Error("useDateTimeFormatSettings must be used within DateTimeFormatProvider");
  }
  return ctx;
}

/**
 * Subscribe to the current date/time format preferences. Components that
 * render dates via lib/datetime formatters should call this so they re-render
 * when the preference changes.
 */
export function useDateTimeFormat(): DateTimeFormatPreferences {
  return useSyncExternalStore(
    subscribeDateTimeFormatPreferences,
    getDateTimeFormatPreferences,
    getDateTimeFormatPreferences,
  );
}
