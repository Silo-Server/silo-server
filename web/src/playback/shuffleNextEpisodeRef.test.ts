import { describe, expect, it } from "vitest";
import type { Shuffle } from "@/api/v2/shuffles";
import { shuffleNextEpisodeRef } from "./shuffleNextEpisodeRef";

function shuffleWith(next: Partial<Shuffle["next"]>): Shuffle {
  return {
    next: { content_id: "episode-2", type: "episode", title: "Two", ...next },
  } as Shuffle;
}

describe("shuffleNextEpisodeRef", () => {
  it("marks a movie so the post-roll screen leaves it unprotected", () => {
    const ref = shuffleNextEpisodeRef(shuffleWith({ type: "movie", content_id: "movie-1" }));
    expect(ref.isMovie).toBe(true);
    expect(ref.watchState).toBeUndefined();
  });

  it("passes an episode's played flag and position", () => {
    const ref = shuffleNextEpisodeRef(
      shuffleWith({
        user_state: { played: true } as Shuffle["next"]["user_state"],
        position_seconds: 30,
      }),
    );
    expect(ref.watchState).toEqual({ played: true, position_seconds: 30 });
  });

  it("protects a still ThumbHash shown under a series backdrop URL", () => {
    const ref = shuffleNextEpisodeRef(
      shuffleWith({
        poster_thumbhash: "still-hash",
        poster_is_episode_still: true,
        backdrop_url: "https://example.invalid/series-backdrop",
        backdrop_thumbhash: "series-hash",
        backdrop_is_episode_still: false,
      }),
    );
    expect(ref.stillUrl).toBe("https://example.invalid/series-backdrop");
    expect(ref.stillThumbhash).toBe("still-hash");
    expect(ref.stillIsEpisodeStill).not.toBe(false);
  });

  it("leaves series artwork clear when both URL and ThumbHash are series art", () => {
    const ref = shuffleNextEpisodeRef(
      shuffleWith({
        poster_url: "https://example.invalid/season-poster",
        poster_thumbhash: "season-hash",
        poster_is_episode_still: false,
        backdrop_is_episode_still: false,
      }),
    );
    expect(ref.stillIsEpisodeStill).toBe(false);
  });
});
