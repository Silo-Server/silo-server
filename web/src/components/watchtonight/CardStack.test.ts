import { describe, expect, it } from "vitest";
import type { SwipeCard } from "@/hooks/queries/recommendations";
import { playTargetForSwipeCard } from "./CardStack";

function card(type: SwipeCard["type"]): SwipeCard {
  return {
    content_id: `${type}-1`,
    type,
    title: "Pick",
    year: 2026,
    genres: [],
    status: "matched",
    rating_imdb: null,
    overview: "",
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    watch_tonight_source: "recommendation",
    cast: [],
  };
}

describe("playTargetForSwipeCard", () => {
  it("routes audiobook cards to the detail player", () => {
    expect(playTargetForSwipeCard(card("audiobook"))).toEqual({
      href: "/item/audiobook-1?play=1",
      isVideo: false,
    });
  });

  it("keeps video cards on the watch route", () => {
    expect(playTargetForSwipeCard(card("movie"))).toEqual({
      href: "/watch/movie-1",
      isVideo: true,
    });
  });
});
