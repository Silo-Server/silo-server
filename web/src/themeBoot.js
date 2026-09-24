/*
 * Applies this device's cached appearance to <html> before first paint.
 *
 * Loaded as a classic, render-blocking script at the top of <head> (see
 * themeBootScript in vite.config.ts). Without it the shell and the app's first
 * frame would paint with index.html's static attributes until ThemeProvider's
 * effects run, showing anyone on a non-default theme, text size, or high
 * contrast the wrong look first.
 *
 * It reproduces the state ThemeProvider starts from before auth resolves: the
 * namespace of the last identity that wrote the appearance cache (see
 * `appearanceCache` in utils/storage.ts), with each value parsed the way
 * hooks/themePreferences.ts parses it. themeBoot.test.tsx checks the two agree,
 * so a new theme id or storage key has to be added here too.
 *
 * The build minifies it but neither bundles nor transpiles it, so keep it plain
 * script syntax with no imports.
 */
(() => {
  const THEME_IDS = [
    "midnight-cinema",
    "cinema-light",
    "cobalt-studio",
    "oxblood-noir",
    "evergreen-studio",
  ];
  const DEFAULT_THEME = "midnight-cinema";

  try {
    const owner = localStorage.getItem("silo-ui-cache-owner") ?? "device";
    const cached = (key) => localStorage.getItem(`${key}:${owner}`);

    const theme = cached("silo-theme");
    const textScale = cached("silo-ui-text-scale");
    const root = document.documentElement;
    root.setAttribute("data-theme", THEME_IDS.includes(theme) ? theme : DEFAULT_THEME);
    root.setAttribute(
      "data-text-scale",
      textScale === "large" || textScale === "x-large" ? textScale : "default",
    );
    root.setAttribute(
      "data-text-weight",
      cached("silo-ui-text-weight") === "strong" ? "strong" : "default",
    );
    root.setAttribute(
      "data-high-contrast",
      cached("silo-ui-high-contrast") === "true" ? "true" : "false",
    );
  } catch {
    // Storage is unavailable (blocked site data, some private modes): keep the
    // static default from index.html. ThemeProvider takes over on mount.
  }
})();
