import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  isFirefoxEngine,
  isWebKitEngine,
  useImmediateSidebarCollapse,
} from "./useImmediateSidebarCollapse";

const SAFARI_USER_AGENT =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.6 Safari/605.1.15";
const ORION_USER_AGENT =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.5 Safari/605.1.15 Orion/0.99.135";
const WEBKIT_GTK_USER_AGENT =
  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15";
const CHROME_USER_AGENT =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/151.0.0.0 Safari/537.36";
const EDGE_USER_AGENT = `${CHROME_USER_AGENT} Edg/151.0.0.0`;
const FIREFOX_USER_AGENT = "Mozilla/5.0 (X11; Linux x86_64; rv:149.0) Gecko/20100101 Firefox/149.0";
const FIREFOX_IOS_USER_AGENT =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/149.0 Mobile/15E148 Safari/605.1.15";

function stubUserAgent(userAgent: string) {
  vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(userAgent);
}

function stubFrames() {
  let nextId = 1;
  const queue: Array<{ callback: FrameRequestCallback; id: number }> = [];
  const cancelled = new Set<number>();
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    const id = nextId++;
    queue.push({ callback, id });
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => cancelled.add(id));
  return {
    get pending() {
      return queue.filter(({ id }) => !cancelled.has(id)).length;
    },
    async frame() {
      let entry = queue.shift();
      while (entry && cancelled.has(entry.id)) entry = queue.shift();
      expect(entry?.callback, "no frame was requested").toBeTypeOf("function");
      await act(async () => entry!.callback(performance.now()));
    },
  };
}

function stubReducedMotion(reduce: boolean) {
  let matches = reduce;
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  const mediaQuery = {
    get matches() {
      return matches;
    },
    media: "(prefers-reduced-motion: reduce)",
    addEventListener(_type: string, listener: (event: MediaQueryListEvent) => void) {
      listeners.add(listener);
    },
    removeEventListener(_type: string, listener: (event: MediaQueryListEvent) => void) {
      listeners.delete(listener);
    },
    addListener() {},
    removeListener() {},
    onchange: null,
    dispatchEvent: () => false,
  };
  vi.stubGlobal("matchMedia", (query: string) => ({
    ...mediaQuery,
    matches: matches && query.includes("prefers-reduced-motion: reduce"),
    media: query,
    addEventListener: mediaQuery.addEventListener,
    removeEventListener: mediaQuery.removeEventListener,
  }));
  return {
    set(next: boolean) {
      matches = next;
      const event = { matches: next } as MediaQueryListEvent;
      for (const listener of listeners) listener(event);
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("useImmediateSidebarCollapse", () => {
  it("passes the initial state straight through", () => {
    stubFrames();
    stubReducedMotion(false);
    expect(renderHook(() => useImmediateSidebarCollapse(true)).result.current).toBe(true);
  });

  it("keeps Chromium on the established one-frame handoff", async () => {
    const frames = stubFrames();
    stubReducedMotion(false);
    stubUserAgent(CHROME_USER_AGENT);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: false },
      },
    );

    rerender({ collapsed: true });
    expect(result.current).toBe(false);
    expect(frames.pending).toBe(1);

    await frames.frame();
    expect(result.current).toBe(true);
  });

  it("keeps Firefox on the established one-frame handoff", async () => {
    const frames = stubFrames();
    stubReducedMotion(false);
    stubUserAgent(FIREFOX_USER_AGENT);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: false },
      },
    );

    rerender({ collapsed: true });
    await frames.frame();
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(0);
  });

  it.each([
    ["Safari", SAFARI_USER_AGENT],
    ["Orion", ORION_USER_AGENT],
    ["WebKitGTK", WEBKIT_GTK_USER_AGENT],
  ])("keeps the two-frame opening handoff in %s", async (_browser, userAgent) => {
    const frames = stubFrames();
    stubReducedMotion(false);
    stubUserAgent(userAgent);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: false },
      },
    );

    rerender({ collapsed: true });
    expect(result.current).toBe(false);

    await frames.frame();
    expect(result.current).toBe(false);
    expect(frames.pending).toBe(1);

    await frames.frame();
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(0);
  });

  it.each([
    ["Safari", SAFARI_USER_AGENT],
    ["Orion", ORION_USER_AGENT],
    ["WebKitGTK", WEBKIT_GTK_USER_AGENT],
  ])("uses the same two-frame Back handoff in %s", async (_browser, userAgent) => {
    const frames = stubFrames();
    stubReducedMotion(false);
    stubUserAgent(userAgent);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: true },
      },
    );

    rerender({ collapsed: false });
    expect(result.current).toBe(true);

    await frames.frame();
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(1);

    await frames.frame();
    expect(result.current).toBe(false);
    expect(frames.pending).toBe(0);
  });

  it.each([
    ["Chrome", CHROME_USER_AGENT],
    ["Edge", EDGE_USER_AGENT],
    ["Firefox", FIREFOX_USER_AGENT],
  ])("keeps the one-frame Back handoff in %s", async (_browser, userAgent) => {
    const frames = stubFrames();
    stubReducedMotion(false);
    stubUserAgent(userAgent);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: true },
      },
    );

    rerender({ collapsed: false });
    expect(result.current).toBe(true);

    await frames.frame();
    expect(result.current).toBe(false);
    expect(frames.pending).toBe(0);
  });

  it("cancels WebKit's paint-boundary frame when navigation reverses", async () => {
    const frames = stubFrames();
    stubReducedMotion(false);
    stubUserAgent(SAFARI_USER_AGENT);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: false },
      },
    );

    rerender({ collapsed: true });
    await frames.frame();
    expect(frames.pending).toBe(1);

    rerender({ collapsed: false });
    expect(result.current).toBe(false);
    expect(frames.pending).toBe(0);
  });

  it("cancels WebKit's Back paint frame when navigation reverses", async () => {
    const frames = stubFrames();
    stubReducedMotion(false);
    stubUserAgent(ORION_USER_AGENT);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: true },
      },
    );

    rerender({ collapsed: false });
    await frames.frame();
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(1);

    rerender({ collapsed: true });
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(0);
  });

  it("applies reduced motion without scheduling a frame", () => {
    const frames = stubFrames();
    stubReducedMotion(true);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      {
        initialProps: { collapsed: false },
      },
    );

    act(() => rerender({ collapsed: true }));
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(0);
  });

  it("observes preference changes without a stale catch-up transition", () => {
    const frames = stubFrames();
    const motion = stubReducedMotion(false);
    const { result, rerender } = renderHook(
      ({ collapsed }) => useImmediateSidebarCollapse(collapsed),
      { initialProps: { collapsed: false } },
    );

    act(() => motion.set(true));
    rerender({ collapsed: true });
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(0);

    act(() => motion.set(false));
    expect(result.current).toBe(true);
    expect(frames.pending).toBe(0);
  });
});

describe("isWebKitEngine", () => {
  it.each([SAFARI_USER_AGENT, ORION_USER_AGENT, WEBKIT_GTK_USER_AGENT, FIREFOX_IOS_USER_AGENT])(
    "detects WebKit engine user agents",
    (userAgent) => expect(isWebKitEngine(userAgent)).toBe(true),
  );

  it.each([CHROME_USER_AGENT, EDGE_USER_AGENT, FIREFOX_USER_AGENT])(
    "does not classify Blink or Gecko as WebKit",
    (userAgent) => expect(isWebKitEngine(userAgent)).toBe(false),
  );
});

describe("isFirefoxEngine", () => {
  it("detects desktop Firefox", () => {
    expect(isFirefoxEngine(FIREFOX_USER_AGENT)).toBe(true);
  });

  it.each([
    SAFARI_USER_AGENT,
    ORION_USER_AGENT,
    WEBKIT_GTK_USER_AGENT,
    FIREFOX_IOS_USER_AGENT,
    CHROME_USER_AGENT,
    EDGE_USER_AGENT,
  ])("does not classify other engines as desktop Firefox", (userAgent) =>
    expect(isFirefoxEngine(userAgent)).toBe(false),
  );
});
