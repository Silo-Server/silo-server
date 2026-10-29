/**
 * The colour themes this fork offers, and the device-local choice between them.
 *
 * Upstream Silo ships one theme, fixed by the static `data-theme` on `<html>`.
 * This fork adds WeFlix, makes it the default, and keeps the upstream theme
 * selectable as a fallback. The choice is device-local, not a server setting:
 * nothing here writes to the settings API.
 *
 * `themeBoot.js` reads the same storage key before first paint, so it must stay
 * in step with {@link parseTheme}. `themeBoot.test.tsx` holds the two together.
 */

export type ThemeId = "weflix" | "midnight-cinema";

/** The theme used when this device has never chosen one. */
export const DEFAULT_THEME: ThemeId = "weflix";

/**
 * Where the chosen theme lives. Deliberately not namespaced by identity, unlike
 * the appearance cache in `utils/storage.ts`: a theme is a look for the whole
 * device, and the login screen has to paint in it before anyone is signed in.
 */
export const THEME_STORAGE_KEY = "silo-app-theme";

/** Every theme, in the order the settings control offers them. */
export const THEME_IDS: readonly ThemeId[] = ["weflix", "midnight-cinema"];

export const THEME_LABELS: Record<ThemeId, string> = {
  weflix: "WeFlix",
  "midnight-cinema": "Midnight Cinema",
};

/**
 * The theme a stored value names, or the default when it names none. An unknown
 * value is a miss rather than an error: storage may hold a value written by a
 * newer build, or by hand.
 */
export function parseTheme(value: string | null | undefined): ThemeId {
  return value === "weflix" || value === "midnight-cinema" ? value : DEFAULT_THEME;
}

/** Storage can be unavailable (blocked site data, some private modes). */
export function readStoredTheme(): ThemeId {
  try {
    return parseTheme(localStorage.getItem(THEME_STORAGE_KEY));
  } catch {
    return DEFAULT_THEME;
  }
}

export function storeTheme(theme: ThemeId): void {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Storage full or unavailable
  }
}

/** Writes the theme where app.css and themes/weflix.css key off it. */
export function applyThemeToDOM(theme: ThemeId): void {
  document.documentElement.setAttribute("data-theme", theme);
}

/**
 * Whether a theme uses the top-bar shell instead of the left rail. The two are
 * one decision: a theme owns both its tokens and its shell, so a theme cannot
 * ask for top-nav tokens over a sidebar layout.
 */
export function isTopNavTheme(theme: ThemeId): boolean {
  return theme === "weflix";
}
