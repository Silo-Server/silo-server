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
  UI_CACHE_OWNER: "silo-ui-cache-owner",
  UI_CUSTOM_CSS: "silo-custom-css",
  CALENDAR_PRESET: "calendar:preset",
} as const;

export type StorageKey = (typeof STORAGE_KEYS)[keyof typeof STORAGE_KEYS];

function getRaw(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function setRaw(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Storage full or unavailable
  }
}

function removeRaw(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // Storage unavailable
  }
}

function get(key: StorageKey): string | null {
  return getRaw(key);
}

function set(key: StorageKey, value: string): void {
  setRaw(key, value);
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
 * Namespace used before anyone has ever signed in on this browser. Values
 * written here are the device's own defaults, not any account's.
 */
const DEVICE_NAMESPACE = "device";

/**
 * Which namespace a read or write belongs to.
 *
 * A known account always uses its own. A `null` owner means auth is still
 * bootstrapping or nobody is signed in, and we fall back to the last account
 * that wrote here so the login screen and the pre-auth first paint keep the
 * look this device last used. That fallback cannot leak into a signed-in
 * session: the moment auth resolves, the owner is exact.
 */
function namespaceFor(owner: string | null): string {
  if (owner !== null) return owner;
  return getRaw(STORAGE_KEYS.UI_CACHE_OWNER) ?? DEVICE_NAMESPACE;
}

/**
 * Device-local mirrors of server-side, per-account settings, so the UI can
 * paint before the settings request resolves. Covers theme, text scale, text
 * weight, high contrast, custom theme tokens, custom CSS, and date/time format.
 *
 * Values are namespaced by the identity that owns them — user id plus active
 * profile id (`silo-theme:7:p1`) — so a second account or a sibling profile
 * signing in on a shared browser simply finds nothing where the first one's
 * values would have been. A miss is just a miss: every caller
 * already parses a missing value into the correct default, and the settings
 * response repopulates the namespace when it lands.
 *
 * Namespacing rather than tagging-and-clearing matters for three reasons.
 * Nothing is ever deleted, so returning to the first account still paints their
 * look with no cold start. There is no shared stamp for a second tab, a stale
 * debounce timer, or an out-of-order effect to race on. And widening ownership
 * — appearance moved from user scope to profile scope with the settings
 * contract, and the owner token widened with it — is a change to
 * `appearanceCacheOwner` alone, which no caller can forget to apply.
 *
 * Values written before namespacing existed sit at the bare key and are simply
 * ignored. Those users take one cold paint, after which the mirror below has
 * repopulated their namespace from the server, which holds all of these
 * settings anyway.
 */
export const appearanceCache = {
  /** The cached value for `owner`, or null when they have none. */
  get(key: StorageKey, owner: string | null): string | null {
    return getRaw(`${key}:${namespaceFor(owner)}`);
  },
  /** Write a value into `owner`'s namespace. */
  set(key: StorageKey, value: string, owner: string | null): void {
    setRaw(`${key}:${namespaceFor(owner)}`, value);
    if (owner !== null) setRaw(STORAGE_KEYS.UI_CACHE_OWNER, owner);
  },
  /**
   * Drop `owner`'s cached value, so the next cold start falls back rather than
   * painting a preference the server no longer holds. Needed because the
   * server's answer is authoritative once loaded: when another client deletes
   * an appearance setting, the effective response simply omits it, and a cache
   * that only ever grows would keep the removed value alive on this browser
   * forever. Deliberately scoped to one owner — clearing another identity's
   * namespace is what the ownership tests exist to prevent.
   */
  remove(key: StorageKey, owner: string | null): void {
    removeRaw(`${key}:${namespaceFor(owner)}`);
  },
};
