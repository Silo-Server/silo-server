// After a deploy, an open tab's code still references the previous build's
// content-hashed chunks; the first lazy navigation then fails with
// "Failed to fetch dynamically imported module". Vite reports that as a
// window "vite:preloadError" event — reload once so the tab picks up the
// current build, with a time guard so a persistently broken chunk cannot
// cause a reload loop.

const GUARD_KEY = "silo:preload-error-reload-at";
const GUARD_WINDOW_MS = 60_000;

interface PreloadErrorReloadDeps {
  reload: () => void;
  now: () => number;
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
}

export function installPreloadErrorReload(deps?: Partial<PreloadErrorReloadDeps>): () => void {
  const reload = deps?.reload ?? (() => window.location.reload());
  const now = deps?.now ?? Date.now;
  const getItem = deps?.getItem ?? ((k: string) => sessionStorage.getItem(k));
  const setItem = deps?.setItem ?? ((k: string, v: string) => sessionStorage.setItem(k, v));

  const handler = (event: Event) => {
    const lastReloadAt = Number(getItem(GUARD_KEY) ?? 0);
    if (now() - lastReloadAt < GUARD_WINDOW_MS) {
      // We already reloaded moments ago and the chunk is still missing;
      // let the failure surface instead of reload-looping.
      return;
    }
    event.preventDefault();
    setItem(GUARD_KEY, String(now()));
    reload();
  };

  window.addEventListener("vite:preloadError", handler);
  return () => window.removeEventListener("vite:preloadError", handler);
}
