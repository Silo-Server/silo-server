import { describe, expect, it, vi } from "vitest";
import { installPreloadErrorReload } from "./reloadOnPreloadError";

function harness(now: () => number) {
  const reload = vi.fn();
  const storage = new Map<string, string>();
  return {
    reload,
    deps: {
      reload,
      now,
      getItem: (k: string) => storage.get(k) ?? null,
      setItem: (k: string, v: string) => storage.set(k, v),
    },
  };
}

describe("installPreloadErrorReload", () => {
  it("reloads when a dynamically imported chunk fails to load", () => {
    const { reload, deps } = harness(() => 1_000_000);
    installPreloadErrorReload(deps);

    window.dispatchEvent(new Event("vite:preloadError"));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("does not reload again within the loop-guard window", () => {
    let clock = 1_000_000;
    const { reload, deps } = harness(() => clock);

    installPreloadErrorReload(deps);
    window.dispatchEvent(new Event("vite:preloadError"));
    expect(reload).toHaveBeenCalledTimes(1);

    // Post-reload page still hits the error seconds later: give up instead
    // of reload-looping.
    clock += 5_000;
    installPreloadErrorReload(deps);
    window.dispatchEvent(new Event("vite:preloadError"));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("reloads again once the guard window has passed (a later deploy)", () => {
    let clock = 1_000_000;
    const { reload, deps } = harness(() => clock);

    installPreloadErrorReload(deps);
    window.dispatchEvent(new Event("vite:preloadError"));
    expect(reload).toHaveBeenCalledTimes(1);

    clock += 10 * 60_000;
    installPreloadErrorReload(deps);
    window.dispatchEvent(new Event("vite:preloadError"));
    expect(reload).toHaveBeenCalledTimes(2);
  });
});
