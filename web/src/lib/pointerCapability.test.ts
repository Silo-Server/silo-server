// TODO: this file redefines document, which is not configurable in VM contexts,
// so it runs on the threads pool (THREADS_TESTS in vite.config.ts). Make it VM-
// safe and drop it from that list.

import { afterEach, describe, expect, it, vi } from "vitest";
import { initPointerCapability, readFinePointer, subscribeFinePointer } from "./pointerCapability";

const ATTR = "data-fine-pointer";

/** A window stub whose matchMedia answer is ours to choose — jsdom has none. */
function windowWith(matches: boolean | undefined) {
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  const win = {
    matchMedia:
      matches === undefined
        ? undefined
        : () => ({
            matches,
            addEventListener: (_: string, fn: (event: MediaQueryListEvent) => void) =>
              listeners.add(fn),
            removeEventListener: (_: string, fn: (event: MediaQueryListEvent) => void) =>
              listeners.delete(fn),
          }),
  } as unknown as Window;
  return {
    win,
    emit: (value: boolean) =>
      listeners.forEach((fn) => fn({ matches: value } as MediaQueryListEvent)),
  };
}

function pointer(type: string, eventType = "pointermove") {
  const event = new Event(eventType, { bubbles: true }) as PointerEvent;
  Object.defineProperty(event, "pointerType", { value: type });
  return event;
}

const cleanups: Array<() => void> = [];
function start(matches: boolean | undefined) {
  const { win, emit } = windowWith(matches);
  const stop = initPointerCapability({ doc: document, win });
  cleanups.push(stop);
  return { stop, emit };
}

afterEach(() => {
  while (cleanups.length) cleanups.pop()?.();
  document.documentElement.removeAttribute(ATTR);
});

describe("initPointerCapability", () => {
  it("treats a pen as a fine pointer", () => {
    start(false);
    document.dispatchEvent(pointer("pen"));
    expect(document.documentElement.getAttribute(ATTR)).toBe("true");
  });

  it("follows the last pointer used across a switch", () => {
    start(false);
    document.dispatchEvent(pointer("mouse"));
    expect(document.documentElement.getAttribute(ATTR)).toBe("true");
    document.dispatchEvent(pointer("touch", "pointerdown"));
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
    document.dispatchEvent(pointer("mouse"));
    expect(document.documentElement.getAttribute(ATTR)).toBe("true");
  });

  it("keeps an observed touch when the media query later promotes", () => {
    // The query is the signal already known to be unreliable on these
    // machines; once a real pointer has been seen it stops being evidence.
    const { emit } = start(false);
    document.dispatchEvent(pointer("touch", "pointerdown"));
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
    emit(true);
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
  });

  it("publishes the value to subscribers", () => {
    const seen: Array<boolean | undefined> = [];
    const unsubscribe = subscribeFinePointer(() => seen.push(readFinePointer()));
    start(false);
    document.dispatchEvent(pointer("mouse"));
    unsubscribe();
    document.dispatchEvent(pointer("touch", "pointerdown"));
    expect(seen).toEqual([false, true]);
  });

  it("reports undefined before anything has been published", () => {
    expect(readFinePointer()).toBeUndefined();
  });

  it("reports undefined where there is no document at all", () => {
    // A default parameter would be evaluated at the call site and throw here,
    // which is what the documented server-rendering fallback forbids.
    vi.stubGlobal("document", undefined);
    try {
      expect(readFinePointer()).toBeUndefined();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("promotes when the media query starts reporting a fine pointer", () => {
    const { emit } = start(false);
    emit(true);
    expect(document.documentElement.getAttribute(ATTR)).toBe("true");
  });

  it("ignores a media query that stops reporting a fine pointer", () => {
    // A detached device says nothing about the pointer in the user's hand, and
    // on the machines this exists for the query is the unreliable signal.
    const { emit } = start(true);
    emit(false);
    expect(document.documentElement.getAttribute(ATTR)).toBe("true");
  });

  it("stops listening after cleanup", () => {
    const { stop } = start(false);
    stop();
    document.dispatchEvent(pointer("mouse"));
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
  });

  it("does not throw when matchMedia is unavailable", () => {
    expect(() => start(undefined)).not.toThrow();
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
  });

  it("writes the attribute only when the value actually changes", () => {
    start(false);
    const spy = vi.spyOn(document.documentElement, "setAttribute");
    document.dispatchEvent(pointer("mouse"));
    document.dispatchEvent(pointer("mouse"));
    expect(spy.mock.calls.filter(([name]) => name === ATTR)).toHaveLength(1);
    spy.mockRestore();
  });
});
