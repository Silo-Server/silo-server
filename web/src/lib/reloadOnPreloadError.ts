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

interface PreloadErrorReloadDeps {
  reload: () => void;
  now: () => number;
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
}

/** Whether the page a reload would load answers right now. */
async function serverAnswers(): Promise<boolean> {
  try {
    const response = await fetch(window.location.href, {
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

export function installPreloadErrorReload(deps?: Partial<PreloadErrorReloadDeps>): () => void {
  const reload = deps?.reload ?? (() => window.location.reload());
  const now = deps?.now ?? Date.now;
  const getItem = deps?.getItem ?? ((k: string) => sessionStorage.getItem(k));
  const setItem = deps?.setItem ?? ((k: string, v: string) => sessionStorage.setItem(k, v));

  // The event is left uncancelled, so the failed import still rejects to its
  // caller (a route's error screen, a local boundary, a warm-up's catch).
  const handler = async () => {
    if (!(await serverAnswers())) return;
    const lastReloadAt = Number(getItem(GUARD_KEY) ?? 0);
    if (now() - lastReloadAt < GUARD_WINDOW_MS) {
      // We already reloaded moments ago and the chunk is still missing;
      // let the failure surface instead of reload-looping.
      return;
    }
    setItem(GUARD_KEY, String(now()));
    reload();
  };

  const listener = () => void handler();
  window.addEventListener("vite:preloadError", listener);
  return () => window.removeEventListener("vite:preloadError", listener);
}
