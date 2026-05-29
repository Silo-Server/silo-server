const STORAGE_KEYS = {
  ACCESS_TOKEN: "access_token",
  REFRESH_TOKEN: "refresh_token",
  PROFILE_ID: "profile_id",
  PROFILE_TOKEN: "profile_token",
  CURRENT_PROFILE: "current_profile",
  DEVICE_ID: "silo-device-id",
  VOLUME: "player-volume",
  MUTED: "player-muted",
  THEME: "silo-theme",
  UI_TEXT_SCALE: "silo-ui-text-scale",
  UI_TEXT_WEIGHT: "silo-ui-text-weight",
  UI_HIGH_CONTRAST: "silo-ui-high-contrast",
  UI_CUSTOM_THEME_VARS: "silo-custom-theme-vars",
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
