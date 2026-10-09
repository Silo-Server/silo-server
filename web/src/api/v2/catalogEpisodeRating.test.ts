import { describe, expect, it } from "vitest";

import { episodeFromV2 } from "@/api/v2/catalog";

// episodeFromV2 builds EpisodeListItem field by field, so a field the server
// sends but the mapper does not copy is dropped silently: the type compiles,
// the API returns the value, and the UI renders nothing. Both episode-list
// fetchers pass every response through this mapper, so the season grid's
// rating badge depends entirely on it.
describe("episodeFromV2 rating", () => {
  const base = {
    content_id: "episode-1",
    season_number: 1,
    episode_number: 2,
    title: "Pilot",
    runtime: 42,
    files: [],
  };

  it("carries the viewer's own rating through the mapper", () => {
    const episode = episodeFromV2({ ...base, user_rating: 4 } as never);

    expect(episode.user_rating).toBe(4);
  });

  it("reports an unrated episode as null rather than undefined", () => {
    const episode = episodeFromV2(base as never);

    // null and undefined both render nothing today, but null is what the
    // sibling item mapper returns, and the grid's check is `!= null`.
    expect(episode.user_rating).toBeNull();
  });
});
