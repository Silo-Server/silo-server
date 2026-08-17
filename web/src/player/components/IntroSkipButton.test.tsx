// @vitest-environment jsdom

import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { IntroSkipButton } from "./IntroSkipButton";

describe("IntroSkipButton", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-17T12:00:00Z"));
  });

  afterEach(() => {
    cleanup();
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("draws its fill from the same wall-clock deadline as the prompt", () => {
    const deadlineMs = Date.now() + 5_000;
    const { container } = render(
      <IntroSkipButton
        onSkip={vi.fn()}
        timer={{ durationMs: 5_000, deadlineMs, remainingMs: 5_000 }}
      />,
    );
    const fill = container.querySelector<HTMLElement>("[aria-hidden=true]");

    expect(fill?.style.width).toBe("0%");
    act(() => vi.advanceTimersByTime(2_500));
    expect(fill?.style.width).toBe("50%");
  });

  it("holds the fill still while the prompt clock is paused", () => {
    const { container } = render(
      <IntroSkipButton
        onSkip={vi.fn()}
        timer={{ durationMs: 5_000, deadlineMs: null, remainingMs: 2_500 }}
      />,
    );
    const fill = container.querySelector<HTMLElement>("[aria-hidden=true]");

    act(() => vi.advanceTimersByTime(2_000));
    expect(fill?.style.width).toBe("50%");
  });

  it("can take focus when the player is using keyboard navigation", () => {
    render(
      <IntroSkipButton
        onSkip={vi.fn()}
        timer={{ durationMs: 5_000, deadlineMs: null, remainingMs: 5_000 }}
        focusOnMount
      />,
    );

    expect(screen.getByRole("button", { name: "Skip Intro" })).toHaveFocus();
  });
});
