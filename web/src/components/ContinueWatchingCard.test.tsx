import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import type { ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SectionItem } from "@/api/types";
import ContinueWatchingCard from "./ContinueWatchingCard";

const spoilerPrefs = vi.hoisted(() => ({ hideImages: false, hideOverviews: false }));
vi.mock("@/hooks/useEpisodeSpoilerPrefs", () => ({ useEpisodeSpoilerPrefs: () => spoilerPrefs }));

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

function card(
  sectionItem: SectionItem,
  props: Pick<ComponentProps<typeof ContinueWatchingCard>, "libraryId" | "quickActionMode"> = {},
) {
  return (
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <ContinueWatchingCard sectionItem={sectionItem} {...props} />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

describe("ContinueWatchingCard", () => {
  beforeEach(() => {
    spoilerPrefs.hideImages = false;
  });
  it.each([true, false, undefined])(
    "uses selected image provenance (%s) for unwatched wide episodes",
    (provenance) => {
      spoilerPrefs.hideImages = true;
      render(
        card({
          ...continueMovie,
          type: "episode",
          item_source: "next_up",
          position_seconds: 0,
          backdrop_is_episode_still: provenance,
        }),
      );
      const image = screen.getByAltText("Apex");
      if (provenance === false) expect(image).not.toHaveClass("blur-xl");
      else expect(image).toHaveClass("blur-xl");
    },
  );

  it("does not offer favorites or the watchlist in a Next Up episode's menu", async () => {
    render(
      card(
        {
          ...continueMovie,
          content_id: "ep-002",
          type: "episode",
          title: "Second",
          series_id: "series-1",
          series_title: "Apex",
          season_number: 1,
          episode_number: 2,
          item_source: "next_up",
          position_seconds: 0,
          user_state: { played: false, is_favorite: false, in_watchlist: false },
        },
        { quickActionMode: "both" },
      ),
    );

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));

    expect(await screen.findByRole("menuitem", { name: "Mark Watched" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: "Add to Favorites" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Add to Watchlist" })).toBeNull();
  });

  it("prefers the backdrop image for section episodes (backdrop_url is the horizontal still)", () => {
    const markup = renderToStaticMarkup(
      card(
        {
          ...continueMovie,
          content_id: "ep-001",
          type: "episode",
          title: "Pilot",
          series_id: "series-1",
          series_title: "Breaking Bad",
          season_number: 1,
          episode_number: 1,
          year: 2008,
          rating_imdb: 9.1,
          overview: "Episode overview",
          position_seconds: 120,
          duration_seconds: 3600,
          poster_url: "/season-poster.jpg",
          backdrop_url: "/episode-backdrop.jpg",
        },
        { quickActionMode: "both" },
      ),
    );

    expect(markup).toContain('src="/episode-backdrop.jpg"');
    expect(markup).not.toContain('src="/season-poster.jpg"');
    // Visibility of the play trigger is owned by app.css, not utility classes.
    expect(markup).toContain("media-card-play-trigger");
    expect(markup).not.toContain("pointer-fine:opacity-0");
    expect(markup).not.toContain("opacity-0");
  });

  it("falls back to the poster when a movie has no backdrop", () => {
    const markup = renderToStaticMarkup(
      card(
        {
          ...continueMovie,
          content_id: "movie-002",
          title: "No Backdrop",
          rating_imdb: 5.0,
          overview: "",
          position_seconds: 0,
          duration_seconds: 6000,
          backdrop_url: "",
        },
        { quickActionMode: "both" },
      ),
    );

    expect(markup).toContain('src="/movie-poster.jpg"');
  });

  it("links ebook continue rows to the reader and shows percent read", () => {
    const markup = renderToStaticMarkup(
      card(
        {
          ...continueMovie,
          content_id: "ebook 001",
          type: "ebook",
          title: "A Reader",
          year: 2026,
          rating_imdb: null,
          overview: "Ebook overview",
          position_seconds: 0.42,
          duration_seconds: 1,
          progress_updated_at: "2026-06-07T00:00:00Z",
          poster_url: "/ebook-cover.jpg",
          backdrop_url: "",
        },
        { libraryId: 12, quickActionMode: "both" },
      ),
    );

    expect(markup).toContain('href="/reader/ebook/ebook%20001?libraryId=12"');
    expect(markup).toContain('href="/item/ebook%20001?libraryId=12"');
    expect(markup).toContain("42% read");
    expect(markup).not.toContain('href="/watch/ebook');
    expect(markup).not.toContain("0 min left");
  });

  it("routes audiobook continue cards to the audiobook detail player", () => {
    const markup = renderToStaticMarkup(
      card(
        {
          ...continueMovie,
          content_id: "book-001",
          type: "audiobook",
          title: "The Way of Kings",
          year: 2010,
          rating_imdb: null,
          overview: "",
          position_seconds: 3600,
          duration_seconds: 14400,
          poster_url: "/book-cover.jpg",
          backdrop_url: "",
        },
        { libraryId: 7, quickActionMode: "both" },
      ),
    );

    expect(markup).toContain('href="/item/book-001?libraryId=7&amp;play=1"');
    expect(markup).not.toContain('href="/watch/book-001');
  });
});
