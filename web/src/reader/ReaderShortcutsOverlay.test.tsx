// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ReaderShortcutsOverlay from "./ReaderShortcutsOverlay";

describe("ReaderShortcutsOverlay", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
  });

  it("autofocuses the Close button", async () => {
    await act(async () => {
      root.render(<ReaderShortcutsOverlay onClose={vi.fn()} />);
    });

    const closeButton = container.querySelector("button");
    expect(document.activeElement).toBe(closeButton);
  });

  it("pulls focus back into the dialog on Tab", async () => {
    await act(async () => {
      root.render(<ReaderShortcutsOverlay onClose={vi.fn()} />);
    });

    const closeButton = container.querySelector<HTMLButtonElement>("button")!;
    const dialog = container.querySelector<HTMLElement>('[role="dialog"]')!;

    // Simulate focus having ended up outside the dialog (e.g. via a stray
    // browser action) — the only way to exercise the trap in jsdom, which
    // does not implement native Tab focus movement.
    const outsideButton = document.createElement("button");
    document.body.appendChild(outsideButton);
    outsideButton.focus();
    expect(document.activeElement).toBe(outsideButton);

    act(() => {
      dialog.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }),
      );
    });

    expect(document.activeElement).toBe(closeButton);
    document.body.removeChild(outsideButton);
  });

  it("keeps focus on the Close button when it is both the first and last stop", async () => {
    await act(async () => {
      root.render(<ReaderShortcutsOverlay onClose={vi.fn()} />);
    });

    const closeButton = container.querySelector<HTMLButtonElement>("button")!;
    const dialog = container.querySelector<HTMLElement>('[role="dialog"]')!;
    closeButton.focus();

    act(() => {
      dialog.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "Tab",
          shiftKey: true,
          bubbles: true,
          cancelable: true,
        }),
      );
    });

    expect(document.activeElement).toBe(closeButton);
  });
});
