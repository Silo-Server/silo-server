import { afterEach, describe, expect, it } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useFinePointer } from "./useFinePointer";
import { initPointerCapability } from "@/lib/pointerCapability";

const ATTR = "data-fine-pointer";
const cleanups: Array<() => void> = [];

function init(matches: boolean) {
  const win = { matchMedia: () => ({ matches, addEventListener() {}, removeEventListener() {} }) };
  const stop = initPointerCapability({ doc: document, win: win as unknown as Window });
  cleanups.push(stop);
}

function mouseMove() {
  const event = new Event("pointermove", { bubbles: true }) as PointerEvent;
  Object.defineProperty(event, "pointerType", { value: "mouse" });
  document.dispatchEvent(event);
}

afterEach(() => {
  while (cleanups.length) cleanups.pop()?.();
  document.documentElement.removeAttribute(ATTR);
});

describe("useFinePointer", () => {
  it("falls back when no capability has been published", () => {
    expect(renderHook(() => useFinePointer(true)).result.current).toBe(true);
    expect(renderHook(() => useFinePointer(false)).result.current).toBe(false);
  });

  it("reports the published capability over the fallback", () => {
    init(false);
    expect(renderHook(() => useFinePointer(true)).result.current).toBe(false);
  });

  it("re-renders when an observed mouse overrides the media query", () => {
    // The regression test for the React half of this bug: on the affected
    // machines the query says no fine pointer, so anything mounted from it
    // stayed absent even after a mouse was in use.
    init(false);
    const { result } = renderHook(() => useFinePointer(true));
    expect(result.current).toBe(false);
    act(() => mouseMove());
    expect(result.current).toBe(true);
  });
});
