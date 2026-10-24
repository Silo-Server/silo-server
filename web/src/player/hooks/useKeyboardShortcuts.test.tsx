import { fireEvent, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
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

it("leaves Space to a focused player menu item", () => {
  const handlePlayPause = vi.fn();
  renderHook(() =>
    useKeyboardShortcuts(
      { current: document.createElement("video") },
      vi.fn(),
      handlePlayPause,
      { back: vi.fn(), forward: vi.fn() },
      vi.fn(),
      vi.fn(),
      undefined,
      true,
    ),
  );
  const menu = document.createElement("div");
  menu.setAttribute("role", "menu");
  const item = document.createElement("button");
  menu.appendChild(item);
  document.body.appendChild(menu);

  expect(fireEvent.keyDown(item, { key: " " })).toBe(true);
  expect(handlePlayPause).not.toHaveBeenCalled();
  fireEvent.keyDown(document.body, { key: " " });
  expect(handlePlayPause).toHaveBeenCalledOnce();
  menu.remove();
});

describe("with a remote", () => {
  function setup(visible: boolean) {
    const container = document.createElement("div");
    const control = document.createElement("button");
    container.appendChild(control);
    document.body.appendChild(container);
    const remote = {
      container: { current: container },
      visible,
      reveal: vi.fn(),
      focusTransport: vi.fn(),
      back: vi.fn(),
    };
    const handlePlayPause = vi.fn();
    const skip = { back: vi.fn(), forward: vi.fn() };
    renderHook(() =>
      useKeyboardShortcuts(
        { current: document.createElement("video") },
        vi.fn(),
        handlePlayPause,
        skip,
        vi.fn(),
        vi.fn(),
        undefined,
        true,
        remote,
      ),
    );
    return { container, control, remote, handlePlayPause, skip };
  }

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("brings focus onto the controls with Up and Down instead of changing volume", () => {
    const { remote } = setup(false);
    expect(fireEvent.keyDown(document.body, { key: "ArrowDown" })).toBe(false);
    expect(remote.focusTransport).toHaveBeenCalledOnce();
    expect(remote.reveal).toHaveBeenCalled();
  });

  it("leaves arrows to spatial navigation while a visible control has focus", () => {
    const { control, remote, skip } = setup(true);
    expect(fireEvent.keyDown(control, { key: "ArrowLeft" })).toBe(true);
    expect(fireEvent.keyDown(control, { key: "ArrowUp" })).toBe(true);
    expect(skip.back).not.toHaveBeenCalled();
    expect(remote.focusTransport).not.toHaveBeenCalled();
  });

  it("seeks when the focused control is hidden", () => {
    const { control, skip } = setup(false);
    fireEvent.keyDown(control, { key: "ArrowLeft" });
    expect(skip.back).toHaveBeenCalledOnce();
  });

  it("plays or pauses on Enter only when no control has focus", () => {
    const { control, handlePlayPause } = setup(true);
    expect(fireEvent.keyDown(control, { key: "Enter" })).toBe(true);
    expect(handlePlayPause).not.toHaveBeenCalled();
    fireEvent.keyDown(document.body, { key: "Enter" });
    expect(handlePlayPause).toHaveBeenCalledOnce();
  });

  it("leaves on Back unless a dialog over the player has focus", () => {
    const { container, remote } = setup(true);
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const button = document.createElement("button");
    dialog.appendChild(button);
    container.appendChild(dialog);

    fireEvent.keyDown(button, { key: "Backspace" });
    expect(remote.back).not.toHaveBeenCalled();
    fireEvent.keyDown(document.body, { key: "Backspace" });
    fireEvent.keyDown(document.body, { key: "GoBack" });
    expect(remote.back).toHaveBeenCalledTimes(2);
  });
});
