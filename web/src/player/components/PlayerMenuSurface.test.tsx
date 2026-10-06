// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlayerMenuSurface } from "./PlayerMenuSurface";

function mockPointer(coarse: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({
      matches: coarse,
      media: "(pointer: coarse)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  );
}

describe("PlayerMenuSurface", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("keeps the supplied anchored popover wrapper for fine pointers", () => {
    mockPointer(false);
    render(
      <PlayerMenuSurface className="desktop-popover" onClose={vi.fn()}>
        Item
      </PlayerMenuSurface>,
    );
    expect(screen.getByRole("menu")).toHaveClass("desktop-popover");
    expect(screen.queryByRole("button", { name: "Close menu" })).toBeNull();
  });

  it("renders a dismissible bottom sheet for coarse pointers", () => {
    mockPointer(true);
    const onClose = vi.fn();
    render(
      <PlayerMenuSurface className="desktop-popover" onClose={onClose}>
        Item
      </PlayerMenuSurface>,
    );
    expect(screen.getByRole("menu")).toHaveClass("fixed", "bottom-0", "rounded-t-2xl");
    fireEvent.click(screen.getByRole("button", { name: "Close menu" }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("closes on a pointer press outside its anchor for fine pointers", () => {
    mockPointer(false);
    const onClose = vi.fn();
    function Harness() {
      const anchorRef = useRef<HTMLDivElement>(null);
      return (
        <>
          <div ref={anchorRef}>
            <button type="button">Trigger</button>
            <PlayerMenuSurface className="desktop-popover" onClose={onClose} anchorRef={anchorRef}>
              <button type="button">Item</button>
            </PlayerMenuSurface>
          </div>
          <button type="button">Outside</button>
        </>
      );
    }
    render(<Harness />);

    fireEvent.pointerDown(screen.getByRole("button", { name: "Item" }));
    fireEvent.pointerDown(screen.getByRole("button", { name: "Trigger" }));
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.pointerDown(screen.getByRole("button", { name: "Outside" }));
    expect(onClose).toHaveBeenCalledOnce();
  });
});
