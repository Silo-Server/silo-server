import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NowListening } from "./NowListening";
import { makePlayback, makePrefs } from "./playerTestUtils";

describe("NowListening", () => {
  it("renders title, author, narrator, and the current chapter heading", () => {
    render(
      <NowListening
        contentId="book-1"
        title="Project Hail Mary"
        author="Andy Weir"
        narrator="Ray Porter"
        posterUrl="/p.jpg"
        playback={makePlayback({
          currentChapter: {
            index: 6,
            title: "The Astrophage",
            start_seconds: 0,
            end_seconds: 100,
            source: "embedded",
          },
        })}
        prefs={makePrefs()}
        onCollapse={vi.fn()}
      />,
    );
    expect(screen.getByRole("heading", { name: "Project Hail Mary" })).toBeInTheDocument();
    expect(screen.getByText("Andy Weir")).toBeInTheDocument();
    expect(screen.getByText(/Ray Porter/)).toBeInTheDocument();
    expect(screen.getByText("The Astrophage")).toBeInTheDocument();
  });

  it("calls onCollapse when the collapse button is clicked", async () => {
    const onCollapse = vi.fn();
    render(
      <NowListening
        contentId="book-1"
        title="X"
        posterUrl=""
        playback={makePlayback()}
        prefs={makePrefs()}
        onCollapse={onCollapse}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /back to player/i }));
    expect(onCollapse).toHaveBeenCalled();
  });

  it("cycles total → remaining → total at 1x speed", async () => {
    render(
      <NowListening
        contentId="book-1"
        title="X"
        posterUrl=""
        playback={makePlayback({ currentTime: 100, duration: 600 })}
        prefs={makePrefs()}
        onCollapse={vi.fn()}
      />,
    );
    const label = () => screen.getByTestId("now-listening-right-time");
    expect(label()).toHaveTextContent("10:00");
    await userEvent.click(label());
    expect(label()).toHaveTextContent("-8:20");
    await userEvent.click(label());
    expect(label()).toHaveTextContent("10:00");
  });

  it("offers remaining time at the current speed when rate is not 1x", async () => {
    render(
      <NowListening
        contentId="book-1"
        title="X"
        posterUrl=""
        playback={makePlayback({ currentTime: 100, duration: 600, rate: 2 })}
        prefs={makePrefs()}
        onCollapse={vi.fn()}
      />,
    );
    const label = () => screen.getByTestId("now-listening-right-time");
    await userEvent.click(label()); // remaining
    expect(label()).toHaveTextContent("-8:20");
    await userEvent.click(label()); // remaining at speed: 500s / 2 = 250s
    expect(label()).toHaveTextContent("-4:10 at 2×");
    await userEvent.click(label());
    expect(label()).toHaveTextContent("10:00");
  });
});
