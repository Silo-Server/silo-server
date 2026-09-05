import { MemoryRouter } from "react-router";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
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
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("names the ken burns tokens literally so tailwind keeps them", () => {
    // Tailwind drops a theme variable it cannot find referenced by name in the
    // scanned source. Building the name from a template literal tree-shook the
    // real values out of the bundle, leaving `var(--animate-ken-burns-a)` to
    // resolve to nothing — the hero simply never animated. app.css still sets
    // both to `none` under prefers-reduced-motion.
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <HeroBanner items={[movieSlide({ backdrop_url: "/backdrop.jpg" })]} />
      </MemoryRouter>,
    );

    expect(markup).toContain('src="/backdrop.jpg"');
    expect(markup).toContain("var(--animate-ken-burns-a)");
  });

  it("keeps outgoing backdrop motion through its fade, then releases it", () => {
    vi.useFakeTimers();
    const { container, unmount } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({ content_id: "movie-1", backdrop_url: "/first.jpg" }),
            movieSlide({ content_id: "movie-2", title: "Second", backdrop_url: "/second.jpg" }),
          ]}
        />
      </MemoryRouter>,
    );
    const images = Array.from(container.querySelectorAll("img"));

    expect(images[0]?.style.animation).toBe("var(--animate-ken-burns-a)");
    expect(images[0]).toHaveClass("will-change-transform");
    expect(images[1]?.style.animation).toBe("none");
    expect(images[1]).not.toHaveClass("will-change-transform");

    act(() => screen.getByRole("button", { name: "Next slide" }).click());

    expect(images[0]?.style.animation).toBe("var(--animate-ken-burns-a)");
    expect(images[0]).not.toHaveClass("will-change-transform");
    expect(images[1]?.style.animation).toBe("var(--animate-ken-burns-b)");
    expect(images[1]).toHaveClass("will-change-transform");

    act(() => vi.advanceTimersByTime(999));
    expect(images[0]?.style.animation).toBe("var(--animate-ken-burns-a)");

    act(() => vi.advanceTimersByTime(1));
    expect(images[0]?.style.animation).toBe("none");
    expect(images[0]).not.toHaveClass("will-change-transform");
    expect(images.filter((image) => image.style.animation !== "none")).toHaveLength(1);

    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("starts only the active and two adjacent backdrop requests, including wraparound", () => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={Array.from({ length: 10 }, (_, index) =>
            movieSlide({ content_id: `movie-${index}`, backdrop_url: `/movie-${index}.jpg` }),
          )}
        />
      </MemoryRouter>,
    );
    const images = Array.from(container.querySelectorAll("img"));

    expect(images.map((image) => image.getAttribute("src"))).toEqual([
      "/movie-0.jpg",
      "/movie-1.jpg",
      "/movie-9.jpg",
    ]);
    expect(images[0]).toHaveAttribute("fetchpriority", "high");
    expect(images.slice(1).every((image) => image.getAttribute("fetchpriority") === "low")).toBe(
      true,
    );
  });

  it("loads neighbors on next and previous navigation and preserves loaded backdrops", () => {
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={Array.from({ length: 10 }, (_, index) =>
            movieSlide({ content_id: `movie-${index}`, backdrop_url: `/movie-${index}.jpg` }),
          )}
        />
      </MemoryRouter>,
    );
    const loadedNeighbor = container.querySelector('img[src="/movie-9.jpg"]')!;
    fireEvent.load(loadedNeighbor);
    const sources = () =>
      Array.from(container.querySelectorAll("img"), (image) => image.getAttribute("src"));

    fireEvent.click(screen.getByRole("button", { name: "Next slide" }));
    expect(sources()).toEqual(["/movie-0.jpg", "/movie-1.jpg", "/movie-2.jpg", "/movie-9.jpg"]);
    expect(container.querySelector('img[src="/movie-9.jpg"]')).toBe(loadedNeighbor);
    expect(container.querySelector('img[fetchpriority="high"]')).toHaveAttribute(
      "src",
      "/movie-1.jpg",
    );

    fireEvent.click(screen.getByRole("button", { name: "Previous slide" }));
    fireEvent.click(screen.getByRole("button", { name: "Previous slide" }));
    expect(sources()).toEqual(["/movie-0.jpg", "/movie-8.jpg", "/movie-9.jpg"]);
    expect(container.querySelector('img[fetchpriority="high"]')).toHaveAttribute(
      "src",
      "/movie-9.jpg",
    );

    fireEvent.click(screen.getByRole("button", { name: "Next slide" }));
    expect(sources()).toEqual(["/movie-0.jpg", "/movie-1.jpg", "/movie-9.jpg"]);
  });

  it("does not eagerly load a replacement URL on a distant loaded slide", () => {
    const slides = Array.from({ length: 10 }, (_, index) =>
      movieSlide({ content_id: `movie-${index}`, backdrop_url: `/movie-${index}.jpg` }),
    );
    const { container, rerender } = render(
      <MemoryRouter>
        <HeroBanner items={slides} />
      </MemoryRouter>,
    );
    fireEvent.load(container.querySelector('img[src="/movie-9.jpg"]')!);
    fireEvent.click(screen.getByRole("button", { name: "Next slide" }));
    rerender(
      <MemoryRouter>
        <HeroBanner
          items={slides.map((slide, index) =>
            index === 9 ? { ...slide, backdrop_url: "/replacement.jpg" } : slide,
          )}
        />
      </MemoryRouter>,
    );

    expect(container.querySelector('img[src="/replacement.jpg"]')).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Previous slide" }));
    const replacement = container.querySelector('img[src="/replacement.jpg"]');
    expect(replacement).toHaveClass("opacity-0");
    fireEvent.load(replacement!);
    expect(replacement).toHaveClass("opacity-100");
  });

  it("does not retain outgoing backdrop motion for reduced motion", () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={[
            movieSlide({ content_id: "movie-1", backdrop_url: "/first.jpg" }),
            movieSlide({ content_id: "movie-2", title: "Second", backdrop_url: "/second.jpg" }),
          ]}
        />
      </MemoryRouter>,
    );
    const images = Array.from(container.querySelectorAll("img"));

    act(() => screen.getByRole("button", { name: "Next slide" }).click());

    expect(images[0]?.style.animation).toBe("none");
    expect(images[0]).not.toHaveClass("will-change-transform");
    expect(images[1]).toHaveClass("will-change-transform");
  });

  it("bounds rapid handoffs to one active and one outgoing backdrop", () => {
    vi.useFakeTimers();
    const { container } = render(
      <MemoryRouter>
        <HeroBanner
          items={Array.from({ length: 12 }, (_, index) =>
            movieSlide({
              content_id: `movie-${index + 1}`,
              title: `Movie ${index + 1}`,
              backdrop_url: `/movie-${index + 1}.jpg`,
            }),
          )}
        />
      </MemoryRouter>,
    );
    const next = screen.getByRole("button", { name: "Next slide" });

    for (let cycle = 0; cycle < 24; cycle++) {
      act(() => next.click());
      const images = Array.from(container.querySelectorAll<HTMLImageElement>(".home-hero img"));
      expect(images.filter((image) => image.style.animation !== "none")).toHaveLength(2);
      expect(container.querySelectorAll(".home-hero img.will-change-transform")).toHaveLength(1);
    }

    act(() => vi.advanceTimersByTime(1000));
    expect(
      Array.from(container.querySelectorAll<HTMLImageElement>(".home-hero img")).filter(
        (image) => image.style.animation !== "none",
      ),
    ).toHaveLength(1);
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
