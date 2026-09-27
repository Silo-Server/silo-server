import { act, cleanup, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { usePlaybackBarHeight } from "./usePlaybackBarHeight";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("tracks bar resizing and releases the reservation when hidden", () => {
  let resize = () => {};
  const disconnect = vi.fn();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: () => void) {
        resize = callback;
      }
      observe() {}
      disconnect = disconnect;
    },
  );
  let height = 100;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
    () => ({ height }) as DOMRect,
  );
  function Bar({ visible }: { visible: boolean }) {
    const ref = usePlaybackBarHeight("watch");
    return visible ? <div ref={ref} style={{ bottom: 12 }} /> : null;
  }
  const view = render(<Bar visible />);
  const reserved = () => document.documentElement.style.getPropertyValue("--watch-bar-height");
  expect(reserved()).toBe("112px");
  height = 160;
  act(() => resize());
  expect(reserved()).toBe("172px");
  view.rerender(<Bar visible={false} />);
  expect(reserved()).toBe("");
  expect(disconnect).toHaveBeenCalledOnce();
});
