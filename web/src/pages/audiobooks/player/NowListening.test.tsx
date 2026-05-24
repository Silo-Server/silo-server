import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NowListening } from "./NowListening";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

function makePlayback(over: Partial<AudiobookPlayback> = {}): AudiobookPlayback {
  return {
    audioRef: { current: null },
    streamUrl: "",
    hasFile: true,
    playing: false,
    currentTime: 0,
    duration: 600,
    buffered: null,
    rate: 1,
    chapters: [],
    currentChapter: null,
    togglePlay: vi.fn(),
    seekTo: vi.fn(),
    skip: vi.fn(),
    setRate: vi.fn(),
    ...over,
  };
}

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
        onCollapse={onCollapse}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /collapse/i }));
    expect(onCollapse).toHaveBeenCalled();
  });

  it("toggles between remaining and total time when the right time is clicked", async () => {
    render(
      <NowListening
        contentId="book-1"
        title="X"
        posterUrl=""
        playback={makePlayback({ currentTime: 100, duration: 600 })}
        onCollapse={vi.fn()}
      />,
    );
    expect(screen.getByTestId("now-listening-right-time")).toHaveTextContent("10:00");
    await userEvent.click(screen.getByTestId("now-listening-right-time"));
    expect(screen.getByTestId("now-listening-right-time")).toHaveTextContent("-8:20");
  });
});
