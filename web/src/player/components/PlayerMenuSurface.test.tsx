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

  it("renders a dismissible bottom sheet for coarse pointers", () => {
    mockPointer(true);
    const onClose = vi.fn();
    render(
      <PlayerMenuSurface className="desktop-popover" onClose={onClose}>
        Item
      </PlayerMenuSurface>,
    );
    expect(screen.getByRole("menu").closest(".hud-sheet")).toHaveClass("fixed", "bottom-0");
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

  it("takes focus to the chosen item on open and gives it back to the opener on close", () => {
    mockPointer(false);
    function Harness({ open }: { open: boolean }) {
      return (
        <>
          <button type="button">Opener</button>
          {open && (
            <PlayerMenuSurface className="desktop-popover" onClose={vi.fn()}>
              <button type="button" role="menuitemradio" aria-checked={false}>
                First
              </button>
              <button type="button" role="menuitemradio" aria-checked>
                Chosen
              </button>
              <button type="button" role="menuitemradio" aria-checked={false}>
                Last
              </button>
            </PlayerMenuSurface>
          )}
        </>
      );
    }
    const { rerender } = render(<Harness open={false} />);
    const opener = screen.getByRole("button", { name: "Opener" });
    opener.focus();

    rerender(<Harness open />);
    const chosen = screen.getByRole("menuitemradio", { name: "Chosen" });
    expect(chosen).toHaveFocus();
    fireEvent.keyDown(chosen, { key: "ArrowDown" });
    expect(screen.getByRole("menuitemradio", { name: "Last" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(screen.getByRole("menuitemradio", { name: "First" })).toHaveFocus();

    rerender(<Harness open={false} />);
    expect(opener).toHaveFocus();
  });
});
