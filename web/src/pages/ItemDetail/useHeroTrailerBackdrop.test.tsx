import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useHeroTrailerBackdrop } from "./useHeroTrailerBackdrop";

function stubMedia({ reduce = false, wide = true } = {}) {
  const listeners = new Map<string, Set<() => void>>();
  vi.stubGlobal("matchMedia", (query: string) => {
    const matches = query.includes("prefers-reduced-motion")
      ? reduce
      : query.includes("min-width")
        ? wide
        : false;
    return {
      matches,
      media: query,
      addEventListener(_type: string, listener: () => void) {
        const set = listeners.get(query) ?? new Set();
        set.add(listener);
        listeners.set(query, set);
      },
      removeEventListener(_type: string, listener: () => void) {
        listeners.get(query)?.delete(listener);
      },
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
      onchange: null,
    } as unknown as MediaQueryList;
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: "visible",
  });
});

describe("useHeroTrailerBackdrop", () => {
  it("enables on a visible wide viewport without reduced motion", async () => {
    stubMedia();
    const { result } = renderHook(() => useHeroTrailerBackdrop());
    await act(async () => {});
    expect(result.current).toBe(true);
  });

  it("disables while the document is hidden", async () => {
    stubMedia();
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "hidden",
    });
    const { result } = renderHook(() => useHeroTrailerBackdrop());
    await act(async () => {});
    expect(result.current).toBe(false);
  });
});
