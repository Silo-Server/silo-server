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
  UI_APPEARANCE_OWNER: "silo-ui-appearance-owner",
  UI_CUSTOM_THEME_OWNER: "silo-ui-custom-theme-owner",
  UI_CUSTOM_CSS: "silo-custom-css",
  CALENDAR_PRESET: "calendar:preset",
} as const;

export type StorageKey = (typeof STORAGE_KEYS)[keyof typeof STORAGE_KEYS];

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

/**
 * A group of localStorage keys that mirror server-side, per-account settings so
 * the UI can paint before the settings request resolves.
 *
 * Every write stamps the account that owns the values, and reads are only
 * honored for that same account: browsers are shared, and a second account
 * signing in must never inherit the first account's cached values.
 */
export interface OwnedCache {
  /**
   * Whether the cached values may be applied to `owner`.
   *
   * A `null` owner (auth still bootstrapping, or signed out) trusts the cache so
   * the warm start still paints. An unstamped cache — written before ownership
   * tagging existed — is never trusted for a known account; those users take a
   * one-time reset instead of a chance of seeing someone else's settings.
   */
  isTrusted: (owner: string | null) => boolean;
  /** The cached value, or null when the cache belongs to a different account. */
  get: (key: StorageKey, owner: string | null) => string | null;
  /** Write a value and stamp `owner` as the cache's owner. */
  set: (key: StorageKey, value: string, owner: string | null) => void;
  /** Drop every cached value and hand the empty cache to `owner`. */
  clear: (owner: string | null) => void;
}

function createOwnedCache(ownerKey: StorageKey, memberKeys: readonly StorageKey[]): OwnedCache {
  function stamp(owner: string | null): void {
    if (owner === null) return;
    set(ownerKey, owner);
  }

  function isTrusted(owner: string | null): boolean {
    return owner === null || get(ownerKey) === owner;
  }

  return {
    isTrusted,
    get: (key, owner) => (isTrusted(owner) ? get(key) : null),
    set: (key, value, owner) => {
      set(key, value);
      stamp(owner);
    },
    clear: (owner) => {
      memberKeys.forEach((key) => remove(key));
      if (owner === null) {
        remove(ownerKey);
      } else {
        stamp(owner);
      }
    },
  };
}

// Each group carries its own owner stamp rather than sharing one. The groups are
// written by different hooks whose effects run in a fixed nesting order, so a
// shared stamp would let whichever hook resolved first vouch for another hook's
// still-stale values.

/** Theme, text scale, text weight and high contrast (written by useTheme). */
export const appearanceCache = createOwnedCache(STORAGE_KEYS.UI_APPEARANCE_OWNER, [
  STORAGE_KEYS.THEME,
  STORAGE_KEYS.UI_TEXT_SCALE,
  STORAGE_KEYS.UI_TEXT_WEIGHT,
  STORAGE_KEYS.UI_HIGH_CONTRAST,
]);

/** Custom theme token overrides and raw CSS (written by useCustomTheme). */
export const customThemeCache = createOwnedCache(STORAGE_KEYS.UI_CUSTOM_THEME_OWNER, [
  STORAGE_KEYS.UI_CUSTOM_THEME_VARS,
  STORAGE_KEYS.UI_CUSTOM_CSS,
]);

/** Date and time format preferences (written by DateTimeFormatProvider). */
export const dateTimeFormatCache = createOwnedCache(STORAGE_KEYS.UI_DATETIME_FORMAT_OWNER, [
  STORAGE_KEYS.UI_DATE_FORMAT,
  STORAGE_KEYS.UI_TIME_FORMAT,
]);
