import type { SectionItem } from "@/api/types";
import type { ComponentProps } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buildWatchRouteRequest } from "@/pages/watchRouteHelpers";
import { takePlaybackIntent } from "@/player/first-frame";
import HeroBanner from "./HeroBanner";
import { formatHeroMetadata } from "./heroMetadata";

const playbackMocks = vi.hoisted(() => ({
  controller: null as null | {
    active: { contentId: string; playing: boolean };
    toggleActivePlayback: () => void;
  },
  toggleActivePlayback: vi.fn(),
}));

const spoilerPrefs = vi.hoisted(() => ({ hideImages: false, hideOverviews: false }));

vi.mock("@/hooks/useEpisodeSpoilerPrefs", () => ({
  useEpisodeSpoilerPrefs: () => spoilerPrefs,
}));

vi.mock("@/hooks/useAmbientColor", () => ({
  useAmbientColor: () => undefined,
}));

const translationMocks = vi.hoisted(() => ({
  hook: vi.fn(),
  state: { translating: false, onTranslate: undefined as undefined | (() => void) },
}));

vi.mock("@/hooks/useOnViewTranslation", () => ({
  useOnViewTranslation: (...args: unknown[]) => {
    translationMocks.hook(...args);
    return translationMocks.state;
  },
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
function hero(props: ComponentProps<typeof HeroBanner>) {
  return (
    <MemoryRouter>
      <HeroBanner {...props} />
    </MemoryRouter>
  );
}

function tenSlides() {
  return Array.from({ length: 10 }, (_, index) =>
    movieSlide({ content_id: `movie-${index}`, backdrop_url: `/movie-${index}.jpg` }),
  );
}

function twoSlides() {
  return [
    movieSlide({ content_id: "movie-1", backdrop_url: "/first.jpg" }),
    movieSlide({ content_id: "movie-2", title: "Second", backdrop_url: "/second.jpg" }),
  ];
}

describe("formatHeroMetadata", () => {
  // A slide with only its year left, so each case adds the one field it checks.
  const bare: Partial<SectionItem> = {
    runtime: undefined,
    duration_seconds: undefined,
    rating_imdb: undefined,
    genres: [],
    content_rating: undefined,
  };

  it.each([Number.NaN, 0, 10.1])("omits invalid IMDb rating %s", (ratingImdb) => {
    expect(formatHeroMetadata(movieSlide({ ...bare, rating_imdb: ratingImdb }))).toEqual([
      { key: "year", label: "2025" },
    ]);
  });

  it.each([
    [
      "keeps a valid upper-bound IMDb rating",
      { rating_imdb: 10 },
      [
        { key: "year", label: "2025" },
        {
          key: "rating",
          label: "IMDb 10.0",
          rating: { source: "imdb", name: "IMDb", score: 100, display: "10.0" },
        },
      ],
    ],
    [
      "falls back to the TMDB score when there is no IMDb score",
      { rating_tmdb: 7.25 },
      [
        { key: "year", label: "2025" },
        {
          key: "rating",
          label: "TMDB 7.3",
          rating: { source: "tmdb", name: "TMDB", score: 72.5, display: "7.3" },
        },
      ],
    ],
    [
      "normalizes genres and content rating before limiting and returns semantic keys",
      {
        genres: [" Drama ", "", "Drama", "  Mystery  ", "Mystery", "Thriller"],
        content_rating: " r ",
      },
      [
        { key: "year", label: "2025" },
        { key: "genre-0", label: "Drama" },
        { key: "genre-1", label: "Mystery" },
        { key: "content-rating", label: "R" },
      ],
    ],
    ["omits a fractional year", { year: 2025.5 }, []],
  ])("%s", (_, overrides: Partial<SectionItem>, expected) => {
    expect(formatHeroMetadata(movieSlide({ ...bare, ...overrides }))).toEqual(expected);
  });
});

describe("HeroBanner", () => {
  beforeEach(() => {
    spoilerPrefs.hideImages = false;
    spoilerPrefs.hideOverviews = false;
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

  it("hides unwatched episode descriptions and reveals started episodes", () => {
    spoilerPrefs.hideImages = true;
    spoilerPrefs.hideOverviews = true;
    const episode = movieSlide({
      type: "episode",
      backdrop_url: "/episode-still.jpg",
      backdrop_is_episode_still: true,
      overview: "The culprit is revealed",
      user_state: { played: false, is_favorite: false, in_watchlist: false },
    });
    const { rerender } = render(hero({ items: [episode] }));
    expect(screen.queryByText("The culprit is revealed")).not.toBeInTheDocument();
    expect(
      document.querySelector('img[src="/episode-still.jpg"]')?.getAttribute("style"),
    ).toContain("blur(24px)");

    rerender(hero({ items: [{ ...episode, position_seconds: 120 }] }));
    expect(screen.getByText("The culprit is revealed")).toBeInTheDocument();
    expect(
      document.querySelector('img[src="/episode-still.jpg"]')?.getAttribute("style"),
    ).not.toContain("blur(");
  });

  it("preserves the series backdrop on an unwatched episode", () => {
    spoilerPrefs.hideImages = true;
    render(
      hero({
        items: [
          movieSlide({
            type: "episode",
            backdrop_url: "/series-art.jpg",
            backdrop_is_episode_still: false,
          }),
        ],
      }),
    );
    expect(document.querySelector("img")?.getAttribute("style")).not.toContain("blur(");
  });

  it("names the ken burns tokens literally so tailwind keeps them", () => {
    // Tailwind drops a theme variable it cannot find referenced by name in the
    // scanned source. Building the name from a template literal tree-shook the
    // real values out of the bundle, leaving `var(--animate-ken-burns-a)` to
    // resolve to nothing — the hero simply never animated. app.css still sets
    // both to `none` under prefers-reduced-motion.
    const markup = renderToStaticMarkup(
      hero({ items: [movieSlide({ backdrop_url: "/backdrop.jpg" })] }),
    );

    expect(markup).toContain('src="/backdrop.jpg"');
    expect(markup).toContain("var(--animate-ken-burns-a)");
  });

  it("keeps outgoing backdrop motion through its fade, then releases it", () => {
    vi.useFakeTimers();
    const { container, unmount } = render(hero({ items: twoSlides() }));
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
    const { container } = render(hero({ items: tenSlides() }));
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
    const { container } = render(hero({ items: tenSlides() }));
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

  it.each([0])("keeps loaded slide %i visible while its URL refreshes", (index) => {
    const slides = tenSlides();
    const { container, rerender } = render(hero({ items: slides }));
    const image = container.querySelector(`img[src="/movie-${index}.jpg"]`)!;
    expect(image).toHaveClass("opacity-0");
    fireEvent.load(image);
    expect(image).toHaveClass("opacity-100");

    rerender(
      hero({
        items: slides.map((slide, slideIndex) =>
          slideIndex === index ? { ...slide, backdrop_url: "/refreshed.jpg" } : slide,
        ),
      }),
    );

    expect(container.querySelector('img[src="/refreshed.jpg"]')).toBe(image);
    expect(image).toHaveClass("opacity-100");
    fireEvent.load(image);
    expect(image).toHaveClass("opacity-100");
    expect(container.querySelectorAll("img")).toHaveLength(3);
  });

  it("does not eagerly load a replacement URL on a distant loaded slide", () => {
    const slides = tenSlides();
    const { container, rerender } = render(hero({ items: slides }));
    const image = container.querySelector('img[src="/movie-9.jpg"]')!;
    fireEvent.load(image);
    fireEvent.click(screen.getByRole("button", { name: "Next slide" }));
    rerender(
      hero({
        items: slides.map((slide, index) =>
          index === 9 ? { ...slide, backdrop_url: "/replacement.jpg" } : slide,
        ),
      }),
    );

    expect(container.querySelector('img[src="/replacement.jpg"]')).toBeNull();
    expect(container.querySelector('img[src="/movie-9.jpg"]')).toBe(image);
    fireEvent.click(screen.getByRole("button", { name: "Previous slide" }));
    const replacement = container.querySelector('img[src="/replacement.jpg"]');
    expect(replacement).toBe(image);
    expect(replacement).toHaveClass("opacity-100");
    fireEvent.load(replacement!);
    expect(replacement).toHaveClass("opacity-100");
    fireEvent.click(screen.getByRole("button", { name: "Next slide" }));
    expect(container.querySelector('img[src="/replacement.jpg"]')).toBe(image);
  });

  it("does not retain outgoing backdrop motion for reduced motion", () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    const { container } = render(hero({ items: twoSlides() }));
    const images = Array.from(container.querySelectorAll("img"));

    act(() => screen.getByRole("button", { name: "Next slide" }).click());

    expect(images[0]?.style.animation).toBe("none");
    expect(images[0]).not.toHaveClass("will-change-transform");
    expect(images[1]).toHaveClass("will-change-transform");
  });

  it("renders editorial metadata in the approved order and caps genres at two", () => {
    const { container } = render(hero({ items: [movieSlide({ duration_seconds: 9_999 })] }));

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

  it.each([
    [
      "falls back to a finite positive duration when catalog runtime is unavailable",
      { runtime: 0, duration_seconds: 7_380 },
      ["2025", "2h 3m", "IMDb 8.1", "Drama", "Mystery", "PG-13"],
    ],
    [
      "falls back when converting a finite catalog runtime overflows",
      { runtime: Number.MAX_VALUE, duration_seconds: 7_380 },
      ["2025", "2h 3m", "IMDb 8.1", "Drama", "Mystery", "PG-13"],
    ],
    [
      "omits a whitespace-only content rating",
      { content_rating: " \t " },
      ["2025", "2h 5m", "IMDb 8.1", "Drama", "Mystery"],
    ],
  ])("%s", (_, overrides: Partial<SectionItem>, expected) => {
    const { container } = render(hero({ items: [movieSlide(overrides)] }));

    expect(heroMetadata(container)).toEqual(expected);
  });

  it.each([
    { runtime: -1, duration_seconds: 0 },
    { runtime: Number.NaN, duration_seconds: Number.NaN },
  ])("omits invalid runtime values: $runtime / $duration_seconds", (runtimeValues) => {
    const { container } = render(
      hero({
        items: [
          movieSlide({
            ...runtimeValues,
            year: 0,
            rating_imdb: null,
            genres: [],
            content_rating: "",
          }),
        ],
      }),
    );

    expect(container.querySelector(".hero-meta-track")).toBeNull();
  });

  it("uses the episode editorial policy for episode slides", () => {
    const { container } = render(
      hero({
        items: [
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
        ],
      }),
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
      hero({
        libraryId: 7,
        items: [
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
        ],
      }),
    );

    expect(markup).toContain('href="/reader/ebook/ebook%201?libraryId=7"');
    expect(markup).toContain('href="/item/ebook%201?libraryId=7"');
    expect(markup).not.toContain('href="/watch/ebook');
    expect(markup).toContain("Read");
  });

  it("routes audiobook play actions to the audiobook detail player", () => {
    const markup = renderToStaticMarkup(hero({ libraryId: 7, items: [audiobookSlide()] }));

    expect(markup).toContain('href="/item/book-1?libraryId=7&amp;play=1"');
    expect(markup).not.toContain('href="/watch/book-1');
    expect(markup).toContain("Listen");
  });

  it("labels audiobook hero actions as resume when progress exists", () => {
    const markup = renderToStaticMarkup(
      hero({ items: [{ ...audiobookSlide(), position_seconds: 120, duration_seconds: 3600 }] }),
    );

    expect(markup).toContain("Resume");
  });

  it("starts the first-frame clock under the request key the watch route rebuilds", async () => {
    render(hero({ libraryId: 7, items: [movieSlide()] }));

    await userEvent.click(screen.getByRole("link", { name: "Play" }));

    const routeRequest = buildWatchRouteRequest("movie-1", new URLSearchParams("libraryId=7"));
    expect(takePlaybackIntent(routeRequest.requestKey)).toEqual(expect.any(Number));
  });

  it("leaves the first-frame clock alone when the Play link opens another tab", () => {
    render(hero({ libraryId: 7, items: [movieSlide()] }));
    const play = screen.getByRole("link", { name: "Play" });
    // The navigation itself is not under test; keep jsdom from following it.
    play.addEventListener("click", (event) => event.preventDefault());

    fireEvent.click(play, { ctrlKey: true });
    fireEvent.click(play, { metaKey: true });
    fireEvent.click(play, { button: 1 });

    const routeRequest = buildWatchRouteRequest("movie-1", new URLSearchParams("libraryId=7"));
    expect(takePlaybackIntent(routeRequest.requestKey)).toBeNull();
  });

  it("pauses the active audiobook from the hero without navigating", async () => {
    playbackMocks.controller = {
      active: { contentId: "book-1", playing: true },
      toggleActivePlayback: playbackMocks.toggleActivePlayback,
    };

    render(hero({ items: [audiobookSlide()] }));

    await userEvent.click(screen.getByRole("link", { name: /pause/i }));

    expect(playbackMocks.toggleActivePlayback).toHaveBeenCalledTimes(1);
  });

  describe("on-view translation", () => {
    afterEach(() => {
      translationMocks.hook.mockClear();
      translationMocks.state = { translating: false, onTranslate: undefined };
    });

    it("translates only the visible slide and refreshes the hero's items", async () => {
      const refresh = vi.fn();
      const visible = movieSlide({ content_id: "movie-1", pending_translation_language: "de" });
      render(
        hero({ items: [visible, movieSlide({ content_id: "movie-2" })], onRefreshItems: refresh }),
      );
      await waitFor(() =>
        expect(translationMocks.hook).toHaveBeenLastCalledWith(visible, {
          onPoll: refresh,
          pendingContentIds: new Set(["movie-1"]),
        }),
      );
    });

    it("keeps translation state across a slide whose overview is hidden", async () => {
      spoilerPrefs.hideOverviews = true;
      const hidden = movieSlide({
        content_id: "episode-1",
        type: "episode",
        overview: "Unwatched plot",
        pending_translation_language: "de",
      });
      render(hero({ items: [hidden] }));
      // The hook stays mounted with no item, so nothing new starts for the
      // hidden overview while an earlier slide's translation keeps polling.
      await waitFor(() =>
        expect(translationMocks.hook).toHaveBeenLastCalledWith(undefined, {
          onPoll: undefined,
          pendingContentIds: new Set(["episode-1"]),
        }),
      );
      expect(screen.queryByText("Unwatched plot")).not.toBeInTheDocument();
    });

    it("does not label a provider overview when only the tagline is AI", async () => {
      render(hero({ items: [movieSlide({ machine_translated_fields: ["tagline"] })] }));
      await waitFor(() => expect(translationMocks.hook).toHaveBeenCalled());
      expect(screen.queryByText("Translated by AI")).not.toBeInTheDocument();
    });

    it("labels a machine-translated description", async () => {
      render(hero({ items: [movieSlide({ machine_translated_fields: ["overview"] })] }));
      expect(await screen.findByText("Translated by AI")).toBeInTheDocument();
    });
  });
});
