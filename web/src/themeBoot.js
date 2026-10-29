/*
 * Applies this device's cached readability preferences to <html> before first
 * paint.
 *
 * Loaded as a classic, render-blocking script at the top of <head> (see
 * themeBootScript in vite.config.ts). Without it the shell and the app's first
 * frame would paint with index.html's static attributes until ThemeProvider's
 * effects run, showing anyone on a larger text size, bolder text, or high
 * contrast the wrong look first. The colour theme is read here for the same
 * reason: index.html's static data-theme names the default, and a device that
 * chose the other theme would otherwise paint the wrong one first.
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

  // Not namespaced by identity, unlike the keys above: the theme is the
  // device's look, and it has to apply on the login screen before anyone is
  // signed in. Only a value naming a theme this build knows is applied, so a
  // stale or hand-edited value leaves index.html's static default in place.
  // Must stay in step with parseTheme in themes/theme.ts.
  const theme = read("silo-app-theme");
  if (theme === "weflix" || theme === "midnight-cinema") {
    root.setAttribute("data-theme", theme);
  }
})();
