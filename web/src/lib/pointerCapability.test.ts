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

function pointer(type: string, eventType = "pointermove", buttons = 0) {
  const event = new Event(eventType, { bubbles: true }) as PointerEvent;
  Object.defineProperty(event, "pointerType", { value: type });
  Object.defineProperty(event, "buttons", { value: buttons });
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
  it("treats a pen that hovers as a fine pointer, contact included", () => {
    start(false);
    document.dispatchEvent(pointer("pen"));
    expect(document.documentElement.getAttribute(ATTR)).toBe("true");
    document.dispatchEvent(pointer("touch", "pointerdown"));
    document.dispatchEvent(pointer("pen", "pointermove", 1));
    expect(document.documentElement.getAttribute(ATTR)).toBe("true");
  });

  it("treats a pen that only makes contact like touch", () => {
    // A pen that can't hover could never uncover hover-only controls.
    start(true);
    document.dispatchEvent(pointer("pen", "pointerdown", 1));
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
    // Contact with the barrel button held reports only that button on Windows.
    document.dispatchEvent(pointer("pen", "pointermove", 2));
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
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

  it("drops the click from a press that began on a hidden hover-only control", () => {
    // A hybrid last used with a mouse hides `.pointer-reveal` controls, which
    // still take hits. A touch there clears the flag and reveals the control
    // before the tap's click arrives, so the click would run an action the user
    // never saw.
    start(true);
    document.dispatchEvent(pointer("mouse"));
    const control = document.createElement("button");
    control.className = "pointer-reveal";
    control.style.opacity = "0";
    const activated = vi.fn();
    control.addEventListener("click", activated);
    document.body.append(control);
    try {
      control.dispatchEvent(pointer("touch", "pointerdown"));
      control.style.opacity = "1";
      control.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1 }));
      expect(activated).not.toHaveBeenCalled();

      // Keyboard activation, and a press on the control once it shows, still work.
      control.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 0 }));
      control.dispatchEvent(pointer("touch", "pointerdown"));
      control.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1 }));
      expect(activated).toHaveBeenCalledTimes(2);
    } finally {
      control.remove();
    }
  });

  it("does not throw when matchMedia is unavailable", () => {
    expect(() => start(undefined)).not.toThrow();
    expect(document.documentElement.getAttribute(ATTR)).toBe("false");
  });
});
