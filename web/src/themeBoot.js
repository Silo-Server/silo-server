/*
 * Applies this device's cached readability preferences to <html> before first
 * paint.
 *
 * Loaded as a classic, render-blocking script at the top of <head> (see
 * themeBootScript in vite.config.ts). Without it the shell and the app's first
 * frame would paint with index.html's static attributes until ThemeProvider's
 * effects run, showing anyone on a larger text size, bolder text, or high
 * contrast the wrong look first. The colour theme needs nothing here: Silo has
 * one base theme, fixed by index.html's data-theme.
 *
 * It reproduces the state ThemeProvider starts from before auth resolves: the
 * namespace of the last identity that wrote the appearance cache (see
 * `appearanceCache` in utils/storage.ts), with each value parsed the way
 * hooks/themePreferences.ts parses it. themeBoot.test.tsx checks that this
 * script and ThemeProvider agree, so a new storage key has to be added here too.
 *
 * The build minifies it but neither bundles nor transpiles it, so keep it plain
 * script syntax with no imports.
 */
(() => {
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
  const textScale = cached("silo-ui-text-scale");
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
