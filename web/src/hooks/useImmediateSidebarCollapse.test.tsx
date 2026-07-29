import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useImmediateSidebarCollapse } from "./useImmediateSidebarCollapse";

function stubFrames() {
  const queue: FrameRequestCallback[] = [];
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    queue.push(callback);
    return queue.length;
  });
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
  return {
    get pending() {
      return queue.length;
    },
    async frame() {
      const callback = queue.shift();
      expect(callback, "no frame was requested").toBeTypeOf("function");
      await act(async () => callback!(performance.now()));
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

  it("starts the visual collapse on the next frame", async () => {
    const frames = stubFrames();
    stubReducedMotion(false);
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
