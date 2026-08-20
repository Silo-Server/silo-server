import { MemoryRouter } from "react-router";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { SectionItem } from "@/api/types";

import HeroBanner from "./HeroBanner";
import { formatHeroMetadata } from "./heroMetadata";

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

function movieSlide(overrides: Partial<SectionItem> = {}): SectionItem {
  return {
    content_id: "movie-1",
    type: "movie",
    title: "Featured Movie",
    year: 2025,
    runtime: 125,
    genres: ["Drama", "Mystery", "Thriller"],
    content_rating: "PG-13",
    status: "matched",
    rating_imdb: 8.1,
    overview: "Overview",
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    ...overrides,
  };
}

function heroMetadata(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll(".hero-meta-track > span")).map(
    (node) => node.textContent ?? "",
  );
}

describe("formatHeroMetadata", () => {
  it("formats episode identity, runtime, and content rating without movie-only metadata", () => {
    expect(
      formatHeroMetadata(
        movieSlide({
          type: "episode",
          season_number: 2,
          episode_number: 3,
          year: 2024,
          runtime: 42,
          rating_imdb: 7.6,
          genres: ["Science Fiction", "Drama"],
          content_rating: " tv-14 ",
        }),
      ),
    ).toEqual([
      { key: "episode-identity", label: "S2 · E3" },
      { key: "runtime", label: "42 min" },
      { key: "content-rating", label: "TV-14" },
    ]);
  });

  it.each([Number.NaN, Number.NEGATIVE_INFINITY, Number.POSITIVE_INFINITY, -1, 0, 10.1])(
    "omits invalid IMDb rating %s",
    (ratingImdb) => {
      expect(
        formatHeroMetadata(
          movieSlide({
            runtime: undefined,
            duration_seconds: undefined,
            rating_imdb: ratingImdb,
            genres: [],
            content_rating: undefined,
          }),
        ),
      ).toEqual([{ key: "year", label: "2025" }]);
    },
  );

  it("keeps a valid upper-bound IMDb rating", () => {
    expect(
      formatHeroMetadata(
        movieSlide({
          runtime: undefined,
          duration_seconds: undefined,
          rating_imdb: 10,
          genres: [],
          content_rating: undefined,
        }),
      ),
    ).toEqual([
      { key: "year", label: "2025" },
      { key: "imdb", label: "IMDb 10.0" },
    ]);
  });

  it("normalizes genres and content rating before limiting and returns semantic keys", () => {
    expect(
      formatHeroMetadata(
        movieSlide({
          runtime: undefined,
          duration_seconds: undefined,
          rating_imdb: undefined,
          genres: [" Drama ", "", "Drama", "  Mystery  ", "Mystery", "Thriller"],
          content_rating: " r ",
        }),
      ),
    ).toEqual([
      { key: "year", label: "2025" },
      { key: "genre-0", label: "Drama" },
      { key: "genre-1", label: "Mystery" },
      { key: "content-rating", label: "R" },
    ]);
  });

  it("omits a fractional year", () => {
    expect(
      formatHeroMetadata(
        movieSlide({
          year: 2025.5,
          runtime: undefined,
          duration_seconds: undefined,
          rating_imdb: undefined,
          genres: [],
          content_rating: undefined,
        }),
      ),
    ).toEqual([]);
  });
});

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

  it("renders editorial metadata in the approved order and caps genres at two", () => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({
              duration_seconds: 9_999,
            }),
          ]}
        />
      </MemoryRouter>,
    );

    expect(heroMetadata(container)).toEqual([
      "2025",
      "2h 5m",
      "IMDb 8.1",
      "Drama",
      "Mystery",
      "PG-13",
    ]);
    expect(container).not.toHaveTextContent("Thriller");
    expect(container).not.toHaveTextContent("2h 47m");
  });

  it("falls back to a finite positive duration when catalog runtime is unavailable", () => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({
              runtime: 0,
              duration_seconds: 7_380,
            }),
          ]}
        />
      </MemoryRouter>,
    );

    expect(heroMetadata(container)).toEqual([
      "2025",
      "2h 3m",
      "IMDb 8.1",
      "Drama",
      "Mystery",
      "PG-13",
    ]);
  });

  it("falls back when converting a finite catalog runtime overflows", () => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({
              runtime: Number.MAX_VALUE,
              duration_seconds: 7_380,
            }),
          ]}
        />
      </MemoryRouter>,
    );

    expect(heroMetadata(container)).toEqual([
      "2025",
      "2h 3m",
      "IMDb 8.1",
      "Drama",
      "Mystery",
      "PG-13",
    ]);
  });

  it("omits a whitespace-only content rating", () => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({
              content_rating: " \t ",
            }),
          ]}
        />
      </MemoryRouter>,
    );

    expect(heroMetadata(container)).toEqual(["2025", "2h 5m", "IMDb 8.1", "Drama", "Mystery"]);
  });

  it.each([
    { runtime: -1, duration_seconds: 0 },
    { runtime: 0, duration_seconds: 1 },
    { runtime: Number.NaN, duration_seconds: Number.NaN },
    { runtime: Number.POSITIVE_INFINITY, duration_seconds: Number.POSITIVE_INFINITY },
  ])("omits invalid runtime values: $runtime / $duration_seconds", (runtimeValues) => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({
              ...runtimeValues,
              year: 0,
              rating_imdb: null,
              genres: [],
              content_rating: "",
            }),
          ]}
        />
      </MemoryRouter>,
    );

    expect(container.querySelector(".hero-meta-track")).toBeNull();
  });

  it("uses the episode editorial policy for episode slides", () => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({
              content_id: "episode-1",
              type: "episode",
              title: "Pilot",
              season_number: 2,
              episode_number: 3,
              runtime: undefined,
              duration_seconds: 2_520,
              year: 2024,
              rating_imdb: 7.6,
              genres: ["Science Fiction", "Drama", "Adventure"],
              content_rating: "TV-14",
            }),
          ]}
        />
      </MemoryRouter>,
    );

    expect(heroMetadata(container)).toEqual(["S2 · E3", "42 min", "TV-14"]);
    expect(container).not.toHaveTextContent("2024");
    expect(container).not.toHaveTextContent("IMDb");
    expect(container).not.toHaveTextContent("Science Fiction");
    expect(container).not.toHaveTextContent("Drama");
    expect(container).not.toHaveTextContent("Adventure");
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
