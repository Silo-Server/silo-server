const STORAGE_KEYS = {
  ACCESS_TOKEN: "access_token",
  REFRESH_TOKEN: "refresh_token",
  PROFILE_ID: "profile_id",
  PROFILE_TOKEN: "profile_token",
  CURRENT_PROFILE: "current_profile",
  PROFILE_LAUNCH: "silo-profile-launch",
  DEVICE_ID: "silo-device-id",
  VOLUME: "player-volume",
  MUTED: "player-muted",
  AUDIOBOOK_SKIP_BACK: "audiobook-skip-back",
  AUDIOBOOK_SKIP_FORWARD: "audiobook-skip-forward",
  AUDIOBOOK_SMART_REWIND: "audiobook-smart-rewind",
  AUDIOBOOK_RATES: "audiobook-rates",
  UI_TEXT_SCALE: "silo-ui-text-scale",
  UI_TEXT_WEIGHT: "silo-ui-text-weight",
  UI_HIGH_CONTRAST: "silo-ui-high-contrast",
  UI_DATE_FORMAT: "silo-ui-date-format",
  UI_TIME_FORMAT: "silo-ui-time-format",
  UI_CACHE_OWNER: "silo-ui-cache-owner",
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

function getSession(key: string): string | null {
  try {
    return sessionStorage.getItem(key);
  } catch {
    return null;
  }
}

function setSession(key: string, value: string): void {
  try {
    sessionStorage.setItem(key, value);
  } catch {
    // Storage full or unavailable
  }
}

function removeSession(key: string): void {
  try {
    sessionStorage.removeItem(key);
  } catch {
    // Storage unavailable
  }
}

/**
 * The active profile choice: which profile, its PIN proof, and the profile
 * itself. Where it lives depends on the tab's profile scope below.
 */
const PROFILE_SELECTION_KEYS: ReadonlySet<string> = new Set([
  STORAGE_KEYS.PROFILE_ID,
  STORAGE_KEYS.PROFILE_TOKEN,
  STORAGE_KEYS.CURRENT_PROFILE,
]);

/**
 * Whether this tab shares the browser's remembered profile ("shared", in
 * localStorage) or keeps its own ("tab", in sessionStorage). A browser set to
 * ask who is watching gives every new tab its own choice, so opening a tab
 * never takes the profile away from another one mid-playback.
 *
 * Fixed for the life of the tab on first use, from the launch setting at that
 * moment: changing the setting to "ask" takes effect in new tabs, and
 * {@link adoptTabProfileSelection} moves a tab back to the shared choice.
 */
const PROFILE_SCOPE_KEY = "silo-profile-scope";
/** Prefix for a tab's own profile choice, distinct from the legacy sessionStorage proof key. */
const TAB_PROFILE_PREFIX = "silo-tab-profile:";
/**
 * Changes whenever the signed-in account changes (sign-in, sign-out, a refused
 * session). A tab's own choice records the epoch it was made in and is void
 * once that changes, so signing out in one tab clears the choice in all of them.
 */
const PROFILE_EPOCH_KEY = "silo-profile-epoch";
const TAB_PROFILE_EPOCH_KEY = `${TAB_PROFILE_PREFIX}epoch`;

type ProfileScope = "shared" | "tab";

function profileScope(): ProfileScope {
  const stored = getSession(PROFILE_SCOPE_KEY);
  if (stored === "shared" || stored === "tab") return stored;
  const scope: ProfileScope = getRaw(STORAGE_KEYS.PROFILE_LAUNCH) === "ask" ? "tab" : "shared";
  setSession(PROFILE_SCOPE_KEY, scope);
  return scope;
}

function currentProfileEpoch(): string {
  return getRaw(PROFILE_EPOCH_KEY) ?? "";
}

function clearTabProfileSelection(): void {
  for (const key of PROFILE_SELECTION_KEYS) removeSession(`${TAB_PROFILE_PREFIX}${key}`);
  removeSession(TAB_PROFILE_EPOCH_KEY);
}

function getTabProfileValue(key: string): string | null {
  if (getSession(TAB_PROFILE_EPOCH_KEY) !== currentProfileEpoch()) {
    clearTabProfileSelection();
    return null;
  }
  return getSession(`${TAB_PROFILE_PREFIX}${key}`);
}

function setTabProfileValue(key: string, value: string): void {
  if (getSession(TAB_PROFILE_EPOCH_KEY) !== currentProfileEpoch()) {
    clearTabProfileSelection();
    setSession(TAB_PROFILE_EPOCH_KEY, currentProfileEpoch());
  }
  setSession(`${TAB_PROFILE_PREFIX}${key}`, value);
}

function get(key: StorageKey): string | null {
  if (PROFILE_SELECTION_KEYS.has(key) && profileScope() === "tab") return getTabProfileValue(key);
  return getRaw(key);
}

function set(key: StorageKey, value: string): void {
  if (PROFILE_SELECTION_KEYS.has(key) && profileScope() === "tab") {
    setTabProfileValue(key, value);
    return;
  }
  setRaw(key, value);
}

function remove(key: StorageKey): void {
  if (PROFILE_SELECTION_KEYS.has(key) && profileScope() === "tab") {
    removeSession(`${TAB_PROFILE_PREFIX}${key}`);
    return;
  }
  removeRaw(key);
}

export const storage = { KEYS: STORAGE_KEYS, get, set, remove };

/** True when this tab keeps its own profile choice rather than the browser's. */
export function hasTabProfileScope(): boolean {
  return profileScope() === "tab";
}

/**
 * Moves this tab to the browser's shared profile choice, carrying its own
 * choice over when it has one, for a switch back to remembering the profile.
 */
export function adoptTabProfileSelection(): void {
  if (profileScope() !== "tab") return;
  const values = [...PROFILE_SELECTION_KEYS].map((key) => [key, getTabProfileValue(key)] as const);
  if (values.some(([key, value]) => key === STORAGE_KEYS.PROFILE_ID && value !== null)) {
    for (const [key, value] of values) {
      if (value === null) removeRaw(key);
      else setRaw(key, value);
    }
  }
  clearTabProfileSelection();
  setSession(PROFILE_SCOPE_KEY, "shared");
}

/**
 * The signed-in account changed: drop the shared profile choice and void every
 * tab's own one, so no tab carries a profile across a sign-out or sign-in.
 */
export function endProfileEpoch(): void {
  setRaw(PROFILE_EPOCH_KEY, `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`);
  for (const key of PROFILE_SELECTION_KEYS) removeRaw(key);
  clearTabProfileSelection();
}

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
 * paint before the settings request resolves. Covers text scale, text weight,
 * high contrast, and date/time format.
 *
 * Values are namespaced by the identity that owns them — user id plus active
 * profile id (`silo-ui-text-scale:7:p1`) — so a second account or a sibling profile
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
