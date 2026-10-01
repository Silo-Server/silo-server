import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { SectionItem } from "@/api/types";
import ContinueWatchingCard from "./ContinueWatchingCard";

const startPlayback = () => {};

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback }),
}));

const continueMovie: SectionItem = {
  content_id: "movie-001",
  type: "movie",
  title: "Apex",
  year: 2024,
  genres: [],
  status: "matched",
  rating_imdb: 6.5,
  overview: "Movie overview",
  item_source: "continue_watching",
  position_seconds: 600,
  duration_seconds: 7200,
  progress_updated_at: "2026-03-07T00:00:00Z",
  poster_url: "/movie-poster.jpg",
  poster_thumbhash: "",
  backdrop_url: "/movie-backdrop.jpg",
  backdrop_thumbhash: "",
  logo_url: "",
};

describe("ContinueWatchingCard", () => {
  it("prefers the backdrop image for section episodes (backdrop_url is the horizontal still)", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            sectionItem={{
              content_id: "ep-001",
              type: "episode",
              title: "Pilot",
              series_id: "series-1",
              series_title: "Breaking Bad",
              season_number: 1,
              episode_number: 1,
              year: 2008,
              genres: [],
              status: "matched",
              rating_imdb: 9.1,
              overview: "Episode overview",
              item_source: "continue_watching",
              position_seconds: 120,
              duration_seconds: 3600,
              progress_updated_at: "2026-03-07T00:00:00Z",
              poster_url: "/season-poster.jpg",
              poster_thumbhash: "",
              backdrop_url: "/episode-backdrop.jpg",
              backdrop_thumbhash: "",
              logo_url: "",
            }}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('src="/episode-backdrop.jpg"');
    expect(markup).not.toContain('src="/season-poster.jpg"');
    // Visibility of the play trigger is owned by app.css, not utility classes.
    expect(markup).toContain("media-card-play-trigger");
    expect(markup).not.toContain("pointer-fine:opacity-0");
    expect(markup).not.toContain("opacity-0");
  });

  it("prefers the backdrop image for movies (poster_url is a vertical poster)", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            sectionItem={{
              content_id: "movie-001",
              type: "movie",
              title: "Apex",
              year: 2024,
              genres: [],
              status: "matched",
              rating_imdb: 6.5,
              overview: "Movie overview",
              item_source: "continue_watching",
              position_seconds: 600,
              duration_seconds: 7200,
              progress_updated_at: "2026-03-07T00:00:00Z",
              poster_url: "/movie-poster.jpg",
              poster_thumbhash: "",
              backdrop_url: "/movie-backdrop.jpg",
              backdrop_thumbhash: "",
              logo_url: "",
              user_state: {
                played: true,
                is_favorite: false,
                in_watchlist: false,
              },
            }}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('src="/movie-backdrop.jpg"');
    expect(markup).not.toContain('src="/movie-poster.jpg"');
    expect(markup).toContain('aria-label="Mark Unwatched"');
    expect(markup).toContain("lucide-eye");
    expect(markup).toContain("text-emerald-400");
  });

  it("falls back to the poster when a movie has no backdrop", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            sectionItem={{
              content_id: "movie-002",
              type: "movie",
              title: "No Backdrop",
              year: 2024,
              genres: [],
              status: "matched",
              rating_imdb: 5.0,
              overview: "",
              item_source: "continue_watching",
              position_seconds: 0,
              duration_seconds: 6000,
              progress_updated_at: "2026-03-07T00:00:00Z",
              poster_url: "/movie-poster.jpg",
              poster_thumbhash: "",
              backdrop_url: "",
              backdrop_thumbhash: "",
              logo_url: "",
            }}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('src="/movie-poster.jpg"');
  });

  it("shows only the watched eye shortcut on poster-shaped continue cards", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            variant="poster"
            sectionItem={{
              content_id: "movie-poster-1",
              type: "movie",
              title: "Poster Movie",
              year: 2025,
              genres: [],
              status: "matched",
              rating_imdb: 7.2,
              overview: "",
              item_source: "continue_watching",
              position_seconds: 300,
              duration_seconds: 6000,
              progress_updated_at: "2026-03-07T00:00:00Z",
              poster_url: "/poster-movie.jpg",
              poster_thumbhash: "",
              backdrop_url: "/poster-movie-backdrop.jpg",
              backdrop_thumbhash: "",
              logo_url: "",
              user_state: {
                played: false,
                is_favorite: false,
                in_watchlist: false,
              },
            }}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('aria-label="Mark Watched"');
    expect(markup).toContain("lucide-eye-off");
    expect(markup).not.toContain('aria-label="Add to favorites"');
  });

  it("links ebook continue rows to the reader and shows percent read", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            sectionItem={{
              content_id: "ebook 001",
              type: "ebook",
              title: "A Reader",
              year: 2026,
              genres: [],
              status: "matched",
              rating_imdb: null,
              overview: "Ebook overview",
              item_source: "continue_watching",
              position_seconds: 0.42,
              duration_seconds: 1,
              progress_updated_at: "2026-06-07T00:00:00Z",
              poster_url: "/ebook-cover.jpg",
              poster_thumbhash: "",
              backdrop_url: "",
              backdrop_thumbhash: "",
              logo_url: "",
            }}
            libraryId={12}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/reader/ebook/ebook%20001?libraryId=12"');
    expect(markup).toContain('href="/item/ebook%20001?libraryId=12"');
    expect(markup).toContain("42% read");
    expect(markup).not.toContain('href="/watch/ebook');
    expect(markup).not.toContain("0 min left");
  });

  it("renders separate watch and item links alongside episodic metadata", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            detail={{
              content_id: "ep-001",
              type: "episode",
              title: "Pilot",
              overview: "Episode overview",
              versions: [],
              subtitles: [],
              intro: null,
              credits: null,
              genres: [],
              cast: [],
              crew: [],
              studios: [],
              networks: [],
              countries: [],
              poster_url: "",
              poster_thumbhash: "",
              backdrop_url: "/episode-backdrop.jpg",
              backdrop_thumbhash: "",
              logo_url: "",
              release_date: null,
              runtime: 42,
              year: 0,
              content_rating: "",
              status: "matched",
              rating_imdb: null,
              rating_tmdb: null,
              rating_rt_critic: null,
              rating_rt_audience: null,
              imdb_id: "",
              tmdb_id: "",
              tvdb_id: "",
              first_air_date: null,
              last_air_date: null,
              season_count: null,
              series_id: "series-1",
              series_title: "Breaking Bad",
              season_number: 1,
              episode_number: 1,
              user_state: {
                played: false,
                is_favorite: false,
                in_watchlist: false,
              },
            }}
            progress={{
              media_item_id: "ep-001",
              position_seconds: 120,
              duration_seconds: 3600,
              completed: false,
              updated_at: "2026-03-07T00:00:00Z",
            }}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/watch/ep-001"');
    expect(markup).toContain('href="/item/ep-001"');
    expect(markup).toContain('href="/item/series-1"');
    expect(markup).toContain('aria-label="Play Breaking Bad"');
    expect(markup).toContain("Breaking Bad");
    expect(markup).toContain("Season 1 Episode 1");
    expect(markup).toContain("Pilot");
    expect(markup).toContain("58 min left");
    expect(markup).toContain("More actions");
    expect(markup).toContain('aria-label="Mark Watched"');
    expect(markup).toContain("lucide-eye-off");
  });

  it("links the poster to the item page and reserves playback for the play button", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            sectionItem={{
              content_id: "movie-001",
              type: "movie",
              title: "Apex",
              year: 2024,
              genres: [],
              status: "matched",
              rating_imdb: 6.5,
              overview: "Movie overview",
              item_source: "continue_watching",
              position_seconds: 600,
              duration_seconds: 7200,
              progress_updated_at: "2026-03-07T00:00:00Z",
              poster_url: "/movie-poster.jpg",
              poster_thumbhash: "",
              backdrop_url: "/movie-backdrop.jpg",
              backdrop_thumbhash: "",
              logo_url: "",
            }}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    // The poster link renders first and must navigate to the item page; the
    // watch href is reserved for the explicit play button.
    const posterLinkIndex = markup.indexOf('href="/item/movie-001"');
    const playLinkIndex = markup.indexOf('href="/watch/movie-001"');
    expect(posterLinkIndex).toBeGreaterThan(-1);
    expect(playLinkIndex).toBeGreaterThan(-1);
    expect(posterLinkIndex).toBeLessThan(playLinkIndex);
    expect(markup).toContain('aria-label="Play Apex"');
  });

  it("routes audiobook continue cards to the audiobook detail player", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            sectionItem={{
              content_id: "book-001",
              type: "audiobook",
              title: "The Way of Kings",
              year: 2010,
              genres: [],
              status: "matched",
              rating_imdb: null,
              overview: "",
              item_source: "continue_watching",
              position_seconds: 3600,
              duration_seconds: 14400,
              progress_updated_at: "2026-03-07T00:00:00Z",
              poster_url: "/book-cover.jpg",
              poster_thumbhash: "",
              backdrop_url: "",
              backdrop_thumbhash: "",
              logo_url: "",
            }}
            libraryId={7}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/item/book-001?libraryId=7&amp;play=1"');
    expect(markup).not.toContain('href="/watch/book-001');
  });

  function renderCard() {
    const queryClient = new QueryClient();
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard sectionItem={continueMovie} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    return screen.getByAltText("Apex").closest("a");
  }

  it("always points the artwork at the item detail page", () => {
    expect(renderCard()).toHaveAttribute("href", "/item/movie-001");
    // The caption also reaches the detail page.
    expect(screen.getByText("Apex").closest("a")).toHaveAttribute("href", "/item/movie-001");
  });
});
