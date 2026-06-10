import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MiniBar } from "./MiniBar";
import { makeChapters, makePlayback, makePrefs } from "./playerTestUtils";

describe("MiniBar", () => {
  it("renders the title", () => {
    render(
      <MiniBar
        contentId="book-1"
        title="Project Hail Mary"
        playback={makePlayback()}
        prefs={makePrefs()}
      />,
    );
    expect(screen.getByText("Project Hail Mary")).toBeInTheDocument();
  });

  it("calls togglePlay when the center button is clicked", async () => {
    const togglePlay = vi.fn();
    render(
      <MiniBar
        contentId="book-1"
        title="X"
        playback={makePlayback({ togglePlay })}
        prefs={makePrefs()}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /^(Play|Pause)$/ }));
    expect(togglePlay).toHaveBeenCalled();
  });

  it("calls onClose when the close button is clicked", async () => {
    const onClose = vi.fn();
    render(
      <MiniBar
        contentId="book-1"
        title="X"
        playback={makePlayback()}
        prefs={makePrefs()}
        onClose={onClose}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /close player/i }));
    expect(onClose).toHaveBeenCalled();
  });

  it("renders the current chapter title under the book title", () => {
    render(
      <MiniBar
        contentId="book-1"
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
        prefs={makePrefs()}
      />,
    );
    expect(screen.getByText("The Astrophage")).toBeInTheDocument();
  });

  it("omits the chapter row when no current chapter is known", () => {
    render(
      <MiniBar
        contentId="book-1"
        title="X"
        playback={makePlayback({ currentChapter: null })}
        prefs={makePrefs()}
      />,
    );
    expect(screen.queryByTestId("minibar-chapter-title")).not.toBeInTheDocument();
  });

  it("invokes onExpand when the cover tile is clicked", async () => {
    const onExpand = vi.fn();
    render(
      <MiniBar
        contentId="book-1"
        title="X"
        posterUrl="/p.jpg"
        playback={makePlayback()}
        prefs={makePrefs()}
        onExpand={onExpand}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /open now listening/i }));
    expect(onExpand).toHaveBeenCalled();
  });

  it("skips by the configured intervals", async () => {
    const skip = vi.fn();
    render(
      <MiniBar
        contentId="book-1"
        title="X"
        playback={makePlayback({ skip })}
        prefs={makePrefs({ skipBack: 15, skipForward: 60 })}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Back 15 seconds" }));
    expect(skip).toHaveBeenCalledWith(-15);
    await userEvent.click(screen.getByRole("button", { name: "Forward 60 seconds" }));
    expect(skip).toHaveBeenCalledWith(60);
  });

  it("shows chapter prev/next buttons only when chapters exist and wires them up", async () => {
    const prevChapter = vi.fn();
    const nextChapter = vi.fn();
    const chapters = makeChapters([0, 300], 600);
    const { rerender } = render(
      <MiniBar
        contentId="book-1"
        title="X"
        playback={makePlayback({ chapters, currentChapter: chapters[0], prevChapter, nextChapter })}
        prefs={makePrefs()}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Previous chapter" }));
    expect(prevChapter).toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Next chapter" }));
    expect(nextChapter).toHaveBeenCalled();

    rerender(
      <MiniBar contentId="book-1" title="X" playback={makePlayback()} prefs={makePrefs()} />,
    );
    expect(screen.queryByRole("button", { name: "Previous chapter" })).not.toBeInTheDocument();
  });

  it("disables next chapter at the last chapter", () => {
    const chapters = makeChapters([0, 300], 600);
    render(
      <MiniBar
        contentId="book-1"
        title="X"
        playback={makePlayback({ chapters, currentChapter: chapters[1] })}
        prefs={makePrefs()}
      />,
    );
    expect(screen.getByRole("button", { name: "Next chapter" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Previous chapter" })).toBeEnabled();
  });
});
