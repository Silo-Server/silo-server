import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useDwellPrefetch } from "./useDwellPrefetch";

function Card({ prefetch }: { prefetch: () => void }) {
  const handlers = useDwellPrefetch(prefetch);
  return (
    <div data-testid="card" {...handlers}>
      <a href="/item/a">Open</a>
    </div>
  );
}

function setSaveData(saveData: boolean | undefined) {
  Object.defineProperty(navigator, "connection", {
    configurable: true,
    value: saveData === undefined ? undefined : { saveData },
  });
}

describe("useDwellPrefetch", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    setSaveData(undefined);
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

  // jsdom never matches :focus-visible, so these stand in for the browser's
  // keyboard-versus-pointer focus heuristic.
  function focusVisible(visible: boolean) {
    const matches = Element.prototype.matches;
    return vi.spyOn(Element.prototype, "matches").mockImplementation(function (
      this: Element,
      selector: string,
    ) {
      return selector === ":focus-visible" ? visible : matches.call(this, selector);
    });
  }

  it("treats keyboard focus as intent and cancels it on blur", () => {
    const spy = focusVisible(true);
    try {
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
    } finally {
      spy.mockRestore();
    }
  });

  it("ignores focus a tap leaves behind", () => {
    const spy = focusVisible(false);
    try {
      const prefetch = vi.fn();
      render(<Card prefetch={prefetch} />);

      act(() => screen.getByRole("link").focus());
      act(() => vi.advanceTimersByTime(500));
      expect(prefetch).not.toHaveBeenCalled();
    } finally {
      spy.mockRestore();
    }
  });

  it("does nothing while the browser asks to save data", () => {
    setSaveData(true);
    const prefetch = vi.fn();
    render(<Card prefetch={prefetch} />);

    const spy = focusVisible(true);
    fireEvent.pointerEnter(screen.getByTestId("card"), { pointerType: "mouse" });
    act(() => screen.getByRole("link").focus());
    act(() => vi.advanceTimersByTime(500));
    spy.mockRestore();
    expect(prefetch).not.toHaveBeenCalled();
  });
});
