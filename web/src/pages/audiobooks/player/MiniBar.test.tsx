import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MiniBar } from "./MiniBar";
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

describe("MiniBar", () => {
  it("renders the title", () => {
    render(<MiniBar title="Project Hail Mary" playback={makePlayback()} />);
    expect(screen.getByText("Project Hail Mary")).toBeInTheDocument();
  });

  it("calls togglePlay when the center button is clicked", async () => {
    const togglePlay = vi.fn();
    render(<MiniBar title="X" playback={makePlayback({ togglePlay })} />);
    await userEvent.click(screen.getByRole("button", { name: /^(Play|Pause)$/ }));
    expect(togglePlay).toHaveBeenCalled();
  });

  it("calls onClose when the close button is clicked", async () => {
    const onClose = vi.fn();
    render(<MiniBar title="X" playback={makePlayback()} onClose={onClose} />);
    await userEvent.click(screen.getByRole("button", { name: /close player/i }));
    expect(onClose).toHaveBeenCalled();
  });

  it("renders the current chapter title under the book title", () => {
    render(
      <MiniBar
        title="X"
        playback={makePlayback({
          currentChapter: {
            index: 6,
            title: "The Astrophage",
            start_seconds: 0,
            end_seconds: 100,
            source: "embedded",
          },
        })}
      />,
    );
    expect(screen.getByText("The Astrophage")).toBeInTheDocument();
  });

  it("omits the chapter row when no current chapter is known", () => {
    render(<MiniBar title="X" playback={makePlayback({ currentChapter: null })} />);
    expect(screen.queryByTestId("minibar-chapter-title")).not.toBeInTheDocument();
  });
});
