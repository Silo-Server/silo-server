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
 * hooks/themePreferences.ts parses it. With no cached theme, the admin's
 * default theme applies: the server stamps it on <html> as data-default-theme
 * when it serves the shell (internal/branding), and BrandingProvider reads it
 * from there until the branding request resolves. themeBoot.test.tsx checks
 * that this script and ThemeProvider agree, so a new theme id or storage key
 * has to be added here too.
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

  // Storage can be unavailable (blocked site data, some private modes); a read
  // that throws is a miss, as in utils/storage.ts.
  const read = (key) => {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  };
  const owner = read("silo-ui-cache-owner") ?? "device";
  const cached = (key) => read(`${key}:${owner}`);

  const root = document.documentElement;
  const serverDefault = root.getAttribute("data-default-theme");
  // Any cached theme counts as a choice, so a stale id paints the built-in
  // default rather than the server's, as in ThemeProvider.
  const theme = cached("silo-theme") ?? serverDefault;
  const textScale = cached("silo-ui-text-scale");
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
})();
