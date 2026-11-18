import { afterEach, describe, expect, it, vi } from "vitest";
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

/** Dispatches Vite's cancelable event, as its preload helper does. */
function failChunk() {
  const event = new Event("vite:preloadError", { cancelable: true });
  window.dispatchEvent(event);
  return event;
}

/**
 * A handler that decides not to reload leaves no state to wait on. The stubbed
 * probe settles within microtasks, so one macrotask turn lets the handler finish.
 */
async function letHandlerFinish() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

async function expectReloads(reload: ReturnType<typeof vi.fn>, times: number) {
  await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(times));
}

function stubServer(ok: boolean) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      if (!ok) throw new TypeError("Failed to fetch");
      return new Response("<!doctype html>", { status: 200 });
    }),
  );
}

describe("installPreloadErrorReload", () => {
  let cleanups: Array<() => void> = [];

  afterEach(() => {
    cleanups.forEach((cleanup) => cleanup());
    cleanups = [];
    vi.unstubAllGlobals();
  });

  it("does not reload again within the loop-guard window", async () => {
    let clock = 1_000_000;
    const { reload, deps } = harness(() => clock);
    stubServer(true);

    cleanups.push(installPreloadErrorReload(deps));
    failChunk();
    await expectReloads(reload, 1);

    // Post-reload page still hits the error seconds later: give up instead
    // of reload-looping.
    clock += 5_000;
    cleanups.push(installPreloadErrorReload(deps));
    failChunk();
    await letHandlerFinish();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("reloads again once the guard window has passed (a later deploy)", async () => {
    let clock = 1_000_000;
    const { reload, deps } = harness(() => clock);
    stubServer(true);

    cleanups.push(installPreloadErrorReload(deps));
    failChunk();
    await expectReloads(reload, 1);

    clock += 10 * 60_000;
    cleanups.push(installPreloadErrorReload(deps));
    failChunk();
    await expectReloads(reload, 2);
  });

  it("leaves the page alone while the server cannot be reached, then reloads once it can", async () => {
    const { reload, deps } = harness(() => 1_000_000);
    cleanups.push(installPreloadErrorReload(deps));

    // Offline or restarting: a reload would land on the browser's error page.
    stubServer(false);
    const event = failChunk();
    await letHandlerFinish();
    expect(reload).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);

    // The unreachable attempt must not use up the loop guard.
    stubServer(true);
    failChunk();
    await expectReloads(reload, 1);
  });
});
