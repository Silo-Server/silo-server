import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MiniBar } from "./MiniBar";
import { makeChapters, makePlayback, makePrefs } from "./playerTestUtils";

describe("MiniBar", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
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
