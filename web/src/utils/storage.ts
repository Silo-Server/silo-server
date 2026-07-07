const STORAGE_KEYS = {
  ACCESS_TOKEN: "access_token",
  REFRESH_TOKEN: "refresh_token",
  PROFILE_ID: "profile_id",
  PROFILE_TOKEN: "profile_token",
  CURRENT_PROFILE: "current_profile",
  DEVICE_ID: "silo-device-id",
  VOLUME: "player-volume",
  MUTED: "player-muted",
  AUDIOBOOK_SKIP_BACK: "audiobook-skip-back",
  AUDIOBOOK_SKIP_FORWARD: "audiobook-skip-forward",
  AUDIOBOOK_SMART_REWIND: "audiobook-smart-rewind",
  AUDIOBOOK_RATES: "audiobook-rates",
  THEME: "silo-theme",
  UI_TEXT_SCALE: "silo-ui-text-scale",
  UI_TEXT_WEIGHT: "silo-ui-text-weight",
  UI_HIGH_CONTRAST: "silo-ui-high-contrast",
  UI_CUSTOM_THEME_VARS: "silo-custom-theme-vars",
  UI_DATE_FORMAT: "silo-ui-date-format",
  UI_TIME_FORMAT: "silo-ui-time-format",
  UI_DATETIME_FORMAT_OWNER: "silo-ui-datetime-format-owner",
  UI_CUSTOM_CSS: "silo-custom-css",
  CALENDAR_PRESET: "calendar:preset",
} as const;

type StorageKey = (typeof STORAGE_KEYS)[keyof typeof STORAGE_KEYS];

function get(key: StorageKey): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function set(key: StorageKey, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Storage full or unavailable
  }
}

function remove(key: StorageKey): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // Storage unavailable
  }
}

export const storage = { KEYS: STORAGE_KEYS, get, set, remove };
