/**
 * How the search shortcut is spelled on this machine: the ⌘ glyph on Apple
 * keyboards, "Ctrl" everywhere else. This is resolved once when the module
 * loads; rendering a shortcut hint does no platform detection or extra work.
 *
 * The handlers themselves accept either modifier — this is a label, not the
 * binding.
 */
export const SEARCH_SHORTCUT_LABEL =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad|iPod/.test(navigator.userAgent)
    ? "⌘ K"
    : "Ctrl K";
