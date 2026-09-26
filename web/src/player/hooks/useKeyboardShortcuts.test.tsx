import { fireEvent, renderHook } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { useKeyboardShortcuts } from "./useKeyboardShortcuts";

it("delegates arrows to directional actions and updates callbacks", () => {
  const video = document.createElement("video");
  const back = vi.fn(),
    forward = vi.fn(),
    changed = vi.fn();
  const { rerender } = renderHook(
    ({ next }) =>
      useKeyboardShortcuts(
        { current: video },
        vi.fn(),
        vi.fn(),
        { back, forward: next },
        vi.fn(),
        vi.fn(),
        undefined,
        true,
      ),
    { initialProps: { next: forward } },
  );
  fireEvent.keyDown(document.body, { key: "ArrowLeft" });
  fireEvent.keyDown(document.body, { key: "ArrowRight" });
  expect(back).toHaveBeenCalledOnce();
  expect(forward).toHaveBeenCalledOnce();
  rerender({ next: changed });
  fireEvent.keyDown(document.body, { key: "ArrowRight" });
  expect(changed).toHaveBeenCalledOnce();
});

it("toggles fullscreen with F", () => {
  const toggleFullscreen = vi.fn();
  renderHook(() =>
    useKeyboardShortcuts(
      { current: document.createElement("video") },
      toggleFullscreen,
      vi.fn(),
      { back: vi.fn(), forward: vi.fn() },
      vi.fn(),
      vi.fn(),
      undefined,
      true,
    ),
  );
  fireEvent.keyDown(document.body, { key: "f" });
  fireEvent.keyDown(document.body, { key: "F" });
  expect(toggleFullscreen).toHaveBeenCalledTimes(2);
});
