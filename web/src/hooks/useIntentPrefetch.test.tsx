import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetIntentPrefetchState } from "@/lib/intentPrefetch";
import { useIntentPrefetch, type IntentPrefetchMode } from "./useIntentPrefetch";

const CARD_RECT = {
  left: 0,
  top: 0,
  right: 200,
  bottom: 300,
  width: 200,
  height: 300,
  x: 0,
  y: 0,
  toJSON: () => ({}),
} as DOMRect;

function Card({
  target,
  prefetch,
  mode = "intent",
}: {
  target: string;
  prefetch: () => Promise<unknown>;
  mode?: IntentPrefetchMode;
}) {
  const handlers = useIntentPrefetch(target, prefetch, mode);
  return (
    <a href={`/item/${target}`} {...handlers}>
      {target}
    </a>
  );
}

function card(name: string) {
  const element = screen.getByRole("link", { name });
  element.getBoundingClientRect = () => CARD_RECT;
  return element;
}

function advance(ms: number) {
  act(() => vi.advanceTimersByTime(ms));
}

function moveTo(element: HTMLElement, x: number, y = 150) {
  fireEvent.pointerMove(element, { pointerType: "mouse", clientX: x, clientY: y, buttons: 0 });
}

/** Enters the card and slows to a stop at x=64 inside it. */
function settleOn(element: HTMLElement) {
  fireEvent.pointerEnter(element, { pointerType: "mouse" });
  moveTo(element, 40);
  advance(16);
  moveTo(element, 60);
  advance(16);
  moveTo(element, 64);
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance", "Date"] });
  resetIntentPrefetchState();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useIntentPrefetch", () => {
  it("ignores a pointer sweeping across a card and fires once it settles there", () => {
    // Prefetching every card a sweep crosses would fetch whole rows.
    const prefetch = vi.fn(() => Promise.resolve());
    render(<Card target="a" prefetch={prefetch} />);
    const element = card("a");

    fireEvent.pointerEnter(element, { pointerType: "mouse" });
    for (let x = 0; x <= 200; x += 25) {
      moveTo(element, x);
      advance(20);
    }
    fireEvent.pointerLeave(element, { pointerType: "mouse" });
    advance(500);
    expect(prefetch).not.toHaveBeenCalled();

    settleOn(element);
    advance(200);
    expect(prefetch).toHaveBeenCalledTimes(1);
  });

  it("does not treat content scrolled under a still pointer as aim", () => {
    const prefetch = vi.fn(() => Promise.resolve());
    render(<Card target="a" prefetch={prefetch} />);
    const element = card("a");

    settleOn(element);
    fireEvent.wheel(window);
    advance(300);
    expect(prefetch).not.toHaveBeenCalled();

    moveTo(element, 66);
    advance(200);
    expect(prefetch).toHaveBeenCalledTimes(1);
  });

  it("does not treat a card that appears under a still pointer as aim", () => {
    const prefetch = vi.fn(() => Promise.resolve());
    render(<Card target="a" prefetch={prefetch} />);
    const element = card("a");

    // The pointer came to rest over whatever was there before the card.
    moveTo(document.body, 64);
    advance(300);
    fireEvent.pointerEnter(element, { pointerType: "mouse" });
    advance(300);
    expect(prefetch).not.toHaveBeenCalled();

    moveTo(element, 66);
    advance(200);
    expect(prefetch).toHaveBeenCalledTimes(1);
  });

  it("prefetches on a plain primary press, not a modified or secondary one", () => {
    const prefetch = vi.fn(() => Promise.resolve());
    render(<Card target="a" prefetch={prefetch} mode="press" />);
    const element = card("a");

    fireEvent.pointerDown(element, { pointerType: "mouse", button: 0, ctrlKey: true });
    fireEvent.pointerDown(element, { pointerType: "mouse", button: 2 });
    expect(prefetch).not.toHaveBeenCalled();

    fireEvent.pointerDown(element, { pointerType: "mouse", button: 0 });
    expect(prefetch).toHaveBeenCalledTimes(1);
  });

  it("holds a speculative prefetch while another runs, but never a press", async () => {
    let finishFirst!: () => void;
    const first = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishFirst = resolve;
        }),
    );
    const second = vi.fn(() => Promise.resolve());
    render(
      <>
        <Card target="a" prefetch={first} />
        <Card target="b" prefetch={second} />
      </>,
    );

    settleOn(card("a"));
    advance(200);
    expect(first).toHaveBeenCalledTimes(1);

    fireEvent.pointerLeave(card("a"), { pointerType: "mouse" });
    settleOn(card("b"));
    advance(200);
    expect(second).not.toHaveBeenCalled();

    // The pointer is still on b, so the next check after a finishes fires.
    await act(async () => finishFirst());
    advance(60);
    expect(second).toHaveBeenCalledTimes(1);

    second.mockClear();
    fireEvent.pointerDown(card("b"), { pointerType: "mouse", button: 0 });
    expect(second).toHaveBeenCalledTimes(1);
  });

  it("skips a swipe that starts on a card and keeps a held touch or a quick tap", () => {
    const prefetch = vi.fn(() => Promise.resolve());
    render(<Card target="a" prefetch={prefetch} mode="press" />);
    const element = card("a");
    const touch = (pointerId: number, x = 50, y = 150) => ({
      pointerType: "touch",
      pointerId,
      button: 0,
      clientX: x,
      clientY: y,
    });

    fireEvent.pointerDown(element, touch(1));
    fireEvent.pointerMove(element, touch(1, 50, 190));
    advance(200);
    expect(prefetch).not.toHaveBeenCalled();

    fireEvent.pointerDown(element, touch(2));
    advance(80);
    expect(prefetch).toHaveBeenCalledTimes(1);

    fireEvent.pointerDown(element, touch(3));
    advance(30);
    fireEvent.pointerUp(element, touch(3));
    expect(prefetch).toHaveBeenCalledTimes(2);

    // A hold that could not prefetch (here it drifted past the hold limit)
    // still leaves the tap its prefetch on release.
    fireEvent.pointerDown(element, touch(4));
    fireEvent.pointerMove(element, touch(4, 50, 157));
    advance(120);
    fireEvent.pointerUp(element, touch(4, 50, 157));
    expect(prefetch).toHaveBeenCalledTimes(3);
  });
});
