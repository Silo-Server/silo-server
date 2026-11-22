/**
 * The Featured hero holds its slideshow while the pointer is over it or
 * keyboard focus is inside it, and resumes only once both have left.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SectionItem } from "@/api/types";
import HeroBanner from "./HeroBanner";

function slide(n: number): SectionItem {
  return {
    content_id: `movie:slide-${n}`,
    type: "movie",
    title: `Slide ${n}`,
    year: 2020,
    genres: [],
    status: "matched",
    rating_imdb: null,
    overview: "",
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
  };
}

function show() {
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter>
        <HeroBanner items={[slide(1), slide(2), slide(3)]} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return screen.getByRole("region");
}

function announced() {
  return document.querySelector('[aria-live="polite"]')?.textContent;
}

/** Presses Tab until the Next slide button has keyboard focus. */
async function tabToNext(u: ReturnType<typeof userEvent.setup>) {
  const nextButton = screen.getByRole("button", { name: "Next slide" });
  for (let i = 0; i < 10 && document.activeElement !== nextButton; i++) await u.tab();
  expect(document.activeElement).toBe(nextButton);
}

/** A mouse crossing the hero's edge: browsers send pointer and mouse events. */
function pointerEnter(hero: HTMLElement) {
  fireEvent.pointerEnter(hero, { pointerType: "mouse" });
  fireEvent.mouseEnter(hero);
}

function pointerLeave(hero: HTMLElement) {
  fireEvent.pointerLeave(hero, { pointerType: "mouse" });
  fireEvent.mouseLeave(hero);
}

function wait(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

beforeEach(() => {
  // Only the slideshow interval is faked, so user-event keeps its own timers.
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("HeroBanner hover and focus hold", () => {
  it("stays held while keyboard focus is inside after the pointer leaves", async () => {
    const hero = show();
    await tabToNext(userEvent.setup());
    pointerEnter(hero);
    pointerLeave(hero);
    wait(20000);
    expect(announced()).toBe("Slide 1");
  });

  it("stays held under the pointer after focus leaves", async () => {
    const hero = show();
    pointerEnter(hero);
    const u = userEvent.setup();
    await tabToNext(u);
    await u.tab();
    expect(hero.contains(document.activeElement)).toBe(false);
    wait(20000);
    expect(announced()).toBe("Slide 1");
  });

  it("resumes once both pointer and focus have left", async () => {
    const hero = show();
    const u = userEvent.setup();
    await tabToNext(u);
    pointerEnter(hero);
    pointerLeave(hero);
    await u.tab();
    wait(8100);
    expect(announced()).toBe("Slide 2");
  });

  it.each([
    ["another control", "Previous slide", "Slide 3", "Slide 1"],
    ["the focused control", "Next slide", "Slide 2", "Slide 3"],
  ])(
    "resumes when the pointer leaves after Tab and a click on %s",
    async (_, button, afterClick, afterLeave) => {
      const hero = show();
      const u = userEvent.setup();
      await tabToNext(u);
      await u.click(screen.getByRole("button", { name: button }));
      expect(announced()).toBe(afterClick);
      pointerLeave(hero);
      wait(8100);
      expect(announced()).toBe(afterLeave);
    },
  );

  it("resumes when the pointer leaves after a right-click on the Play link", async () => {
    const hero = show();
    const u = userEvent.setup();
    pointerEnter(hero);
    await u.pointer({ keys: "[MouseRight]", target: screen.getByRole("link", { name: /play/i }) });
    expect(hero.contains(document.activeElement)).toBe(true);
    pointerLeave(hero);
    wait(8100);
    expect(announced()).toBe("Slide 2");
  });

  it("keeps a focus hold when a screen reader clicks a focused control", async () => {
    const hero = show();
    const u = userEvent.setup();
    await tabToNext(u);
    const nextButton = screen.getByRole("button", { name: "Next slide" });
    fireEvent.mouseDown(nextButton);
    fireEvent.mouseUp(nextButton);
    fireEvent.click(nextButton);
    expect(announced()).toBe("Slide 2");
    // Still a focus hold: a mouse brushing across leaves it held, and
    // tabbing out releases it.
    pointerEnter(hero);
    pointerLeave(hero);
    wait(20000);
    expect(announced()).toBe("Slide 2");
    await u.tab();
    wait(8100);
    expect(announced()).toBe("Slide 3");
  });

  it("resumes when the pointer leaves after Play and a click on Next", async () => {
    const hero = show();
    const u = userEvent.setup();
    await u.click(screen.getByRole("button", { name: "Pause slideshow" }));
    await u.click(screen.getByRole("button", { name: "Play slideshow" }));
    // The pointer stays over the hero: press Next the way a browser does,
    // with no new pointerenter.
    const nextButton = screen.getByRole("button", { name: "Next slide" });
    fireEvent.mouseDown(nextButton);
    act(() => nextButton.focus());
    fireEvent.mouseUp(nextButton);
    fireEvent.click(nextButton);
    expect(announced()).toBe("Slide 2");
    wait(20000);
    expect(announced()).toBe("Slide 2");
    pointerLeave(hero);
    wait(8100);
    expect(announced()).toBe("Slide 3");
  });

  it("stays held under the pointer after Play, Tab, and a click on Next", async () => {
    const hero = show();
    const u = userEvent.setup();
    await u.click(screen.getByRole("button", { name: "Pause slideshow" }));
    await u.click(screen.getByRole("button", { name: "Play slideshow" }));
    await tabToNext(u);
    const nextButton = screen.getByRole("button", { name: "Next slide" });
    fireEvent.mouseDown(nextButton);
    fireEvent.mouseUp(nextButton);
    fireEvent.click(nextButton);
    expect(announced()).toBe("Slide 2");
    wait(20000);
    expect(announced()).toBe("Slide 2");
    pointerLeave(hero);
    wait(8100);
    expect(announced()).toBe("Slide 3");
  });

  it("holds a later keyboard focus after a drag that started in the hero", async () => {
    const hero = show();
    pointerEnter(hero);
    const title = screen.getByRole("heading", { name: "Slide 1" });
    fireEvent.mouseDown(title);
    fireEvent.dragEnd(title);
    // One Shift+Tab from the page lands straight on Next slide.
    await userEvent.setup().tab({ shift: true });
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Next slide" }));
    pointerLeave(hero);
    wait(20000);
    expect(announced()).toBe("Slide 1");
  });

  it("resumes when focus leaves after a tap on a control", async () => {
    const hero = show();
    const nextButton = screen.getByRole("button", { name: "Next slide" });
    // A tap sends a touch pointer, then emulated mouse events with no
    // mouseleave until the next tap elsewhere.
    fireEvent.pointerEnter(hero, { pointerType: "touch" });
    fireEvent.mouseEnter(nextButton);
    fireEvent.mouseDown(nextButton);
    act(() => nextButton.focus());
    fireEvent.mouseUp(nextButton);
    fireEvent.click(nextButton);
    expect(announced()).toBe("Slide 2");
    await userEvent.setup().tab();
    expect(hero.contains(document.activeElement)).toBe(false);
    wait(8100);
    expect(announced()).toBe("Slide 3");
  });

  it("stays held after a right-click on the focused control and the pointer leaving", async () => {
    const hero = show();
    pointerEnter(hero);
    await tabToNext(userEvent.setup());
    const nextButton = screen.getByRole("button", { name: "Next slide" });
    fireEvent.mouseDown(nextButton, { button: 2 });
    fireEvent.contextMenu(nextButton);
    fireEvent.mouseUp(nextButton, { button: 2 });
    pointerLeave(hero);
    wait(20000);
    expect(announced()).toBe("Slide 1");
  });

  it.each([
    ["a press elsewhere", (other: HTMLElement) => fireEvent.pointerDown(other)],
    ["focus elsewhere", (other: HTMLElement) => fireEvent.focusIn(other)],
    ["a tap on the hero's title", () => fireEvent.pointerDown(screen.getByRole("heading"))],
  ])("resumes after a focused control goes without a blur and %s", async (_, trigger) => {
    show();
    await tabToNext(userEvent.setup());
    // Removing a focused element moves focus to the body with no blur.
    const activeElement = vi.spyOn(document, "activeElement", "get").mockReturnValue(document.body);
    // A control elsewhere on the page that keeps its events to itself.
    const other = document.body.appendChild(document.createElement("button"));
    other.addEventListener("pointerdown", (event) => event.stopPropagation());
    other.addEventListener("focusin", (event) => event.stopPropagation());
    trigger(other);
    activeElement.mockRestore();
    other.remove();
    wait(8100);
    expect(announced()).toBe("Slide 2");
  });

  it("stays held after a press elsewhere that leaves focus inside", async () => {
    show();
    await tabToNext(userEvent.setup());
    fireEvent.pointerDown(document.body);
    wait(20000);
    expect(announced()).toBe("Slide 1");
  });
});
