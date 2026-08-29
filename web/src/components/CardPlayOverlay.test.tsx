// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import CardPlayOverlay from "./CardPlayOverlay";

const mocks = vi.hoisted(() => ({ startPlayback: vi.fn() }));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: mocks.startPlayback }),
}));

describe("CardPlayOverlay", () => {
  beforeEach(() => mocks.startPlayback.mockReset());

  it("starts resumable playback in place and preserves the return location", () => {
    render(
      <MemoryRouter initialEntries={["/home?profile=primary"]}>
        <CardPlayOverlay contentId="episode 1" title="Running Show" libraryId={12} />
      </MemoryRouter>,
    );

    const link = screen.getByRole("link", { name: "Play Running Show" });
    expect(link).toHaveAttribute("href", "/watch/episode%201?libraryId=12");
    // Hover/focus reveal lives in the shared media-card-play-trigger rule
    // (app.css) rather than inline pointer-fine variants, so every card
    // surface reveals its play control the same way.
    expect(link.className).toContain("media-card-play-trigger");

    fireEvent.click(link);
    expect(mocks.startPlayback).toHaveBeenCalledWith({
      contentId: "episode 1",
      fileId: undefined,
      libraryId: 12,
      restart: false,
      returnHref: "/home?profile=primary",
    });
  });

  it("leaves modified clicks to the watch link", () => {
    render(
      <MemoryRouter>
        <CardPlayOverlay contentId="episode-1" title="Running Show" />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Play Running Show" }), { ctrlKey: true });
    expect(mocks.startPlayback).not.toHaveBeenCalled();
  });

  it("supports compact posters and notifies the containing surface before playback", () => {
    const onPlaybackStart = vi.fn();
    const parentClick = vi.fn();
    render(
      <MemoryRouter>
        <div onClick={parentClick}>
          <CardPlayOverlay
            contentId="movie-1"
            title="Compact Movie"
            type="movie"
            size="compact"
            onPlaybackStart={onPlaybackStart}
          />
        </div>
      </MemoryRouter>,
    );

    const link = screen.getByRole("link", { name: "Play Compact Movie" });
    expect(link).toHaveAttribute("href", "/watch/movie-1");
    expect(link.className).toContain("h-6 w-6");
    fireEvent.click(link);

    expect(onPlaybackStart).toHaveBeenCalledOnce();
    expect(parentClick).not.toHaveBeenCalled();
    expect(mocks.startPlayback).toHaveBeenCalledWith(
      expect.objectContaining({ contentId: "movie-1", restart: false }),
    );
  });
});
