import { MemoryRouter } from "react-router";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import HeroBanner from "./HeroBanner";

const playbackMocks = vi.hoisted(() => ({
  controller: null as null | {
    active: { contentId: string; playing: boolean };
    toggleActivePlayback: () => void;
  },
  toggleActivePlayback: vi.fn(),
}));

vi.mock("@/hooks/useAmbientColor", () => ({
  useAmbientColor: () => undefined,
}));

vi.mock("@/lib/thumbhash", () => ({
  decodeThumbhash: () => "",
}));

vi.mock("@/pages/audiobooks/player/audiobookPlaybackContext", () => ({
  useAudiobookPlaybackController: () => playbackMocks.controller,
}));

function audiobookSlide() {
  return {
    content_id: "book-1",
    type: "audiobook" as const,
    title: "Featured Audiobook",
    year: 2025,
    genres: ["Fantasy"],
    status: "matched" as const,
    rating_imdb: null,
    overview: "Overview",
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
  };
}

describe("HeroBanner", () => {
  beforeEach(() => {
    playbackMocks.controller = null;
    playbackMocks.toggleActivePlayback.mockClear();
  });

  it("does not render the desktop spotlighting card", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <HeroBanner
          items={[
            {
              content_id: "movie-1",
              type: "movie",
              title: "Featured Movie",
              year: 2025,
              genres: ["Drama"],
              status: "matched",
              rating_imdb: 8.1,
              overview: "Overview",
              poster_url: "",
              poster_thumbhash: "",
              backdrop_url: "",
              backdrop_thumbhash: "",
              logo_url: "",
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("Now spotlighting");
    expect(markup).toContain("Featured Movie");
    expect(markup).toContain("More Info");
  });

  it("routes ebook hero actions to the reader", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <HeroBanner
          libraryId={7}
          items={[
            {
              content_id: "ebook 1",
              type: "ebook",
              title: "Featured Ebook",
              year: 2024,
              genres: ["Mystery"],
              status: "matched",
              rating_imdb: null,
              overview: "Overview",
              poster_url: "",
              poster_thumbhash: "",
              backdrop_url: "",
              backdrop_thumbhash: "",
              logo_url: "",
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/reader/ebook/ebook%201?libraryId=7"');
    expect(markup).toContain('href="/item/ebook%201?libraryId=7"');
    expect(markup).not.toContain('href="/watch/ebook');
    expect(markup).toContain("Read");
  });

  it("routes audiobook play actions to the audiobook detail player", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <HeroBanner
          libraryId={7}
          items={[
            {
              ...audiobookSlide(),
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/book-1?libraryId=7&amp;play=1"');
    expect(markup).not.toContain('href="/watch/book-1');
    expect(markup).toContain("Listen");
  });

  it("labels audiobook hero actions as resume when progress exists", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <HeroBanner
          items={[
            {
              ...audiobookSlide(),
              position_seconds: 120,
              duration_seconds: 3600,
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("Resume");
  });

  it("labels completed audiobook hero actions as listen again", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <HeroBanner
          items={[
            {
              ...audiobookSlide(),
              user_state: {
                played: true,
                is_favorite: false,
                in_watchlist: false,
              },
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("Listen Again");
  });

  it("pauses the active audiobook from the hero without navigating", async () => {
    playbackMocks.controller = {
      active: { contentId: "book-1", playing: true },
      toggleActivePlayback: playbackMocks.toggleActivePlayback,
    };

    render(
      <MemoryRouter>
        <HeroBanner items={[audiobookSlide()]} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("link", { name: /pause/i }));

    expect(playbackMocks.toggleActivePlayback).toHaveBeenCalledTimes(1);
  });
});
