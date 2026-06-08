import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import ContinueWatchingCard from "./ContinueWatchingCard";

const startPlayback = () => {};

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback }),
}));

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
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('src="/episode-backdrop.jpg"');
    expect(markup).not.toContain('src="/season-poster.jpg"');
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
            }}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('src="/movie-backdrop.jpg"');
    expect(markup).not.toContain('src="/movie-poster.jpg"');
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
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('src="/movie-poster.jpg"');
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
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/reader/ebook/ebook%20001?libraryId=12"');
    expect(markup).toContain('href="/item/ebook 001?libraryId=12"');
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
            }}
            progress={{
              media_item_id: "ep-001",
              position_seconds: 120,
              duration_seconds: 3600,
              completed: false,
              updated_at: "2026-03-07T00:00:00Z",
            }}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/watch/ep-001"');
    expect(markup).toContain('href="/item/ep-001"');
    expect(markup).toContain("Breaking Bad");
    expect(markup).toContain("Season 1 Episode 1");
    expect(markup).toContain("Pilot");
    expect(markup).toContain("58 min left");
    expect(markup).toContain("More actions");
  });
});
