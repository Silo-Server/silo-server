// After a deploy, an open tab's code still references the previous build's
// content-hashed chunks; the first lazy navigation then fails with
// "Failed to fetch dynamically imported module". Vite reports that as a
// window "vite:preloadError" event — reload once so the tab picks up the
// current build, with a time guard so a persistently broken chunk cannot
// cause a reload loop.
//
// A chunk also fails when the browser is offline or the server is restarting.
// Reloading then lands on the browser's own error page and loses the page, so
// reload only once the server answers; until then the failure surfaces in the
// app's own error UI.

const GUARD_KEY = "silo:preload-error-reload-at";
const GUARD_WINDOW_MS = 60_000;
const PROBE_TIMEOUT_MS = 5_000;
const STYLESHEET_ATTEMPTS = 10;
const STYLESHEET_RETRY_MS = 2_000;
const STYLESHEET_RETRY_MAX_MS = 30_000;
// Vite's message for a stylesheet a lazy import needed but could not load.
const FAILED_STYLESHEET = /^Unable to preload CSS for (\S+)$/;

interface PreloadErrorReloadDeps {
  reload: () => void;
  now: () => number;
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
}

/** Whether the page a reload would load answers right now. */
async function serverAnswers(): Promise<boolean> {
  try {
    // The path alone: the query can hold a one-time value the page has not
    // consumed yet, and the server answers the same page without it.
    const response = await fetch(window.location.origin + window.location.pathname, {
      cache: "no-store",
      // An auth proxy may redirect to its sign-in page on another origin;
      // following it here would fail on CORS, while a reload follows it fine.
      redirect: "manual",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    return response.ok || response.type === "opaqueredirect";
  } catch {
    return false;
  }
}

function failedStylesheet(event: Event): string | null {
  const message = (event as Event & { payload?: { message?: unknown } }).payload?.message;
  if (typeof message !== "string") return null;
  return FAILED_STYLESHEET.exec(message)?.[1] ?? null;
}

/**
 * Vite marks a stylesheet as requested before it loads and never requests it
 * again, so one that failed without a reload would stay missing from every
 * route that loads later. Add it again until it loads: on reconnect while the
 * browser is offline, otherwise after a growing delay, a bounded number of times.
 */
function restoreStylesheet(href: string, attempt = 0): void {
  if (attempt >= STYLESHEET_ATTEMPTS) return;
  const load = () => {
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.crossOrigin = "";
    link.href = href;
    link.addEventListener(
      "error",
      () => {
        link.remove();
        restoreStylesheet(href, attempt + 1);
      },
      { once: true },
    );
    document.head.append(link);
  };
  if (!navigator.onLine) {
    window.addEventListener("online", load, { once: true });
    return;
  }
  window.setTimeout(load, Math.min(STYLESHEET_RETRY_MS * 2 ** attempt, STYLESHEET_RETRY_MAX_MS));
}

export function installPreloadErrorReload(deps?: Partial<PreloadErrorReloadDeps>): () => void {
  const reload = deps?.reload ?? (() => window.location.reload());
  const now = deps?.now ?? Date.now;
  const getItem = deps?.getItem ?? ((k: string) => sessionStorage.getItem(k));
  const setItem = deps?.setItem ?? ((k: string, v: string) => sessionStorage.setItem(k, v));

  // We already reloaded moments ago and a chunk is still missing; let the
  // failure surface instead of reload-looping.
  const guarded = () => now() - Number(getItem(GUARD_KEY) ?? 0) < GUARD_WINDOW_MS;

  // The event is left uncancelled, so the failed import still rejects to its
  // caller (a route's error screen, a local boundary, a warm-up's catch).
  const handler = async (event: Event) => {
    if (!guarded() && (await serverAnswers()) && !guarded()) {
      setItem(GUARD_KEY, String(now()));
      reload();
      return;
    }
    const stylesheet = failedStylesheet(event);
    if (stylesheet) restoreStylesheet(stylesheet);
  };

  const listener = (event: Event) => void handler(event);
  window.addEventListener("vite:preloadError", listener);
  return () => window.removeEventListener("vite:preloadError", listener);
}
