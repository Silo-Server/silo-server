import { act, fireEvent, render, screen } from "@testing-library/react";
import { createPortal } from "react-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useDwellPrefetch } from "./useDwellPrefetch";

function Card({ prefetch }: { prefetch: () => void }) {
  const handlers = useDwellPrefetch(prefetch);
  return (
    <div data-testid="card" {...handlers}>
      <a href="/item/a">Open</a>
      {createPortal(<button type="button">Menu item</button>, document.body)}
    </div>
  );
}

function setSaveData(saveData: boolean) {
  Object.defineProperty(navigator, "connection", { configurable: true, value: { saveData } });
}

// jsdom never matches :focus-visible, so this stands in for the browser's
// keyboard-versus-pointer focus heuristic.
function focusVisible(visible: boolean) {
  const matches = Element.prototype.matches;
  vi.spyOn(Element.prototype, "matches").mockImplementation(function (
    this: Element,
    selector: string,
  ) {
    return selector === ":focus-visible" ? visible : matches.call(this, selector);
  });
}

describe("useDwellPrefetch", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    Reflect.deleteProperty(navigator, "connection");
  });

  it("prefetches after a mouse rests on the card", () => {
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);

    fireEvent.pointerEnter(screen.getByTestId("card"), { pointerType: "mouse" });
    act(() => vi.advanceTimersByTime(99));
    expect(prefetch).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(prefetch).toHaveBeenCalledOnce();
  });

  it("starts nothing for cards a pointer sweeps across", () => {
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);
    const card = screen.getByTestId("card");

    for (let i = 0; i < 5; i++) {
      fireEvent.pointerEnter(card, { pointerType: "mouse" });
      act(() => vi.advanceTimersByTime(40));
      fireEvent.pointerLeave(card, { pointerType: "mouse" });
    }
    act(() => vi.advanceTimersByTime(500));
    expect(prefetch).not.toHaveBeenCalled();
  });

  it("ignores touch", () => {
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);

    fireEvent.pointerEnter(screen.getByTestId("card"), { pointerType: "touch" });
    act(() => vi.advanceTimersByTime(500));
    expect(prefetch).not.toHaveBeenCalled();
  });

  it("treats keyboard focus as intent and cancels it on blur", () => {
    focusVisible(true);
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);
    const link = screen.getByRole("link");

    act(() => link.focus());
    act(() => vi.advanceTimersByTime(50));
    act(() => link.blur());
    act(() => vi.advanceTimersByTime(500));
    expect(prefetch).not.toHaveBeenCalled();

    act(() => link.focus());
    act(() => vi.advanceTimersByTime(100));
    expect(prefetch).toHaveBeenCalledOnce();
  });

  it("ignores focus a tap leaves behind", () => {
    focusVisible(false);
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);

    act(() => screen.getByRole("link").focus());
    act(() => vi.advanceTimersByTime(500));
    expect(prefetch).not.toHaveBeenCalled();
  });

  it("ignores focus in a menu the card portals out", () => {
    focusVisible(true);
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);

    act(() => screen.getByRole("button", { name: "Menu item" }).focus());
    act(() => vi.advanceTimersByTime(500));
    expect(prefetch).not.toHaveBeenCalled();
  });

  it("does nothing while the browser asks to save data", () => {
    setSaveData(true);
    focusVisible(true);
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);

    fireEvent.pointerEnter(screen.getByTestId("card"), { pointerType: "mouse" });
    act(() => screen.getByRole("link").focus());
    act(() => vi.advanceTimersByTime(500));
    expect(prefetch).not.toHaveBeenCalled();
  });
});
