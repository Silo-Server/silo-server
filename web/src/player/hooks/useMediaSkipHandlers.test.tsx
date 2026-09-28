import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useMediaSkipHandlers } from "./useMediaSkipHandlers";

afterEach(() => vi.unstubAllGlobals());

it("uses configured callbacks even when the OS supplies an offset, updates, and releases ownership", () => {
  const handlers = new Map<string, ((details: { seekOffset: number }) => void) | null>();
  const setActionHandler = vi.fn((name, fn) => handlers.set(name, fn));
  vi.stubGlobal("navigator", { mediaSession: { setActionHandler } });
  const back = vi.fn(),
    forward = vi.fn(),
    changed = vi.fn();
  const { rerender, unmount } = renderHook(
    ({ fn, enabled }) => useMediaSkipHandlers(enabled, back, fn),
    { initialProps: { fn: forward, enabled: true } },
  );
  act(() => handlers.get("seekforward")?.({ seekOffset: 10 }));
  expect(forward).toHaveBeenCalledOnce();
  rerender({ fn: changed, enabled: true });
  act(() => handlers.get("seekforward")?.({ seekOffset: 10 }));
  expect(changed).toHaveBeenCalledOnce();
  rerender({ fn: changed, enabled: false });
  expect(handlers.get("seekforward")).toBeNull();
  expect(handlers.get("seekbackward")).toBeNull();
  unmount();
});

it("tolerates unsupported browser actions", () => {
  vi.stubGlobal("navigator", {
    mediaSession: {
      setActionHandler: () => {
        throw new Error("unsupported");
      },
    },
  });
  const { unmount } = renderHook(() => useMediaSkipHandlers(true, vi.fn(), vi.fn()));
  expect(() => unmount()).not.toThrow();
});
