import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SwipeCard as SwipeCardType } from "@/hooks/queries/recommendations";
import SwipeCard from "./SwipeCard";

const spoilerPrefs = vi.hoisted(() => ({ hideImages: false, hideOverviews: false }));
vi.mock("@/hooks/useEpisodeSpoilerPrefs", () => ({
  useEpisodeSpoilerPrefs: () => spoilerPrefs,
}));

const episode: SwipeCardType = {
  content_id: "episode-1",
  type: "episode",
  title: "The reveal",
  series_title: "Mystery series",
  season_number: 1,
  episode_number: 2,
  year: 2026,
  genres: [],
  content_rating: "TV-PG",
  status: "matched",
  rating_imdb: null,
  overview: "The culprit is revealed",
  poster_url: "",
  poster_thumbhash: "",
  backdrop_is_episode_still: true,
  poster_is_episode_still: false,
  backdrop_url: "/episode-still.jpg",
  backdrop_thumbhash: "",
  logo_url: "",
  user_state: { played: false, is_favorite: false, in_watchlist: false },
  watch_tonight_source: "next_up",
  cast: [],
};

describe("Watch Tonight spoiler protection", () => {
  beforeEach(() => {
    spoilerPrefs.hideImages = true;
    spoilerPrefs.hideOverviews = true;
  });

  it("hides the description when flipping an unwatched Next Up episode", () => {
    render(<SwipeCard card={episode} isTop onAccept={vi.fn()} onReject={vi.fn()} />);
    expect(screen.getByAltText("Mystery series")).toHaveClass("blur-xl");
    fireEvent.click(screen.getByAltText("Mystery series"));
    expect(screen.queryByText(episode.overview)).not.toBeInTheDocument();
  });

  it("preserves fallback series art and protects an old payload without provenance", () => {
    const { rerender } = render(
      <SwipeCard
        card={{ ...episode, backdrop_is_episode_still: false }}
        isTop
        onAccept={vi.fn()}
        onReject={vi.fn()}
      />,
    );
    expect(screen.getByAltText("Mystery series")).not.toHaveClass("blur-xl");
    rerender(
      <SwipeCard
        card={{ ...episode, backdrop_is_episode_still: undefined }}
        isTop
        onAccept={vi.fn()}
        onReject={vi.fn()}
      />,
    );
    expect(screen.getByAltText("Mystery series")).toHaveClass("blur-xl");
  });

  it.each([
    { ...episode, user_state: { played: true, is_favorite: false, in_watchlist: false } },
    { ...episode, position_seconds: 120, watch_tonight_source: "continue_watching" as const },
    { ...episode, type: "movie" as const },
  ])("keeps descriptions for played or started episodes and movies", (card) => {
    render(<SwipeCard card={card} isTop onAccept={vi.fn()} onReject={vi.fn()} />);
    expect(screen.getByText(episode.overview)).toBeInTheDocument();
  });
});
