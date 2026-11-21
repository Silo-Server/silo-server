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
  vi.useRealTimers();
});

describe("HeroBanner hover and focus hold", () => {
  it("stays held while keyboard focus is inside after the pointer leaves", async () => {
    const hero = show();
    await tabToNext(userEvent.setup());
    fireEvent.mouseEnter(hero);
    fireEvent.mouseLeave(hero);
    wait(20000);
    expect(announced()).toBe("Slide 1");
  });

  it("stays held under the pointer after focus leaves", async () => {
    const hero = show();
    fireEvent.mouseEnter(hero);
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
    fireEvent.mouseEnter(hero);
    fireEvent.mouseLeave(hero);
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
      fireEvent.mouseLeave(hero);
      wait(8100);
      expect(announced()).toBe(afterLeave);
    },
  );

  it("resumes when the pointer leaves after a right-click on the Play link", async () => {
    const hero = show();
    const u = userEvent.setup();
    fireEvent.mouseEnter(hero);
    await u.pointer({ keys: "[MouseRight]", target: screen.getByRole("link", { name: /play/i }) });
    expect(hero.contains(document.activeElement)).toBe(true);
    fireEvent.mouseLeave(hero);
    wait(8100);
    expect(announced()).toBe("Slide 2");
  });

  it("stays held when a screen reader clicks a focused control", async () => {
    show();
    await tabToNext(userEvent.setup());
    const nextButton = screen.getByRole("button", { name: "Next slide" });
    fireEvent.mouseDown(nextButton);
    fireEvent.mouseUp(nextButton);
    fireEvent.click(nextButton);
    expect(announced()).toBe("Slide 2");
    wait(20000);
    expect(announced()).toBe("Slide 2");
  });

  it("resumes when the pointer leaves after Play and a click on Next", async () => {
    const hero = show();
    const u = userEvent.setup();
    await u.click(screen.getByRole("button", { name: "Pause slideshow" }));
    await u.click(screen.getByRole("button", { name: "Play slideshow" }));
    // The pointer stays over the hero: press Next the way a browser does,
    // with no new mouseenter.
    const nextButton = screen.getByRole("button", { name: "Next slide" });
    fireEvent.mouseDown(nextButton);
    act(() => nextButton.focus());
    fireEvent.mouseUp(nextButton);
    fireEvent.click(nextButton);
    expect(announced()).toBe("Slide 2");
    fireEvent.mouseLeave(hero);
    wait(8100);
    expect(announced()).toBe("Slide 3");
  });
});
