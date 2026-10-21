// @vitest-environment node

import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { catalogKeys, ebookKeys, episodeKeys, itemKeys, progressKeys } from "@/hooks/queries/keys";
import type { ItemDetail } from "@/api/types";
import { makeItemDetail } from "./itemDetail.test-support";
import { getCachedWatchedInvalidationKeys, getWatchedInvalidationKeys } from "./watchedState";

function makeItem({ type = "movie", ...overrides }: Partial<ItemDetail>) {
  return makeItemDetail(type, { content_id: "item-1", title: "Title", runtime: 120, ...overrides });
}

describe("getWatchedInvalidationKeys", () => {
  it.each([
    [
      "returns the expected related queries for an episode target",
      { content_id: "episode-4", type: "episode", series_id: "series-1", season_number: 2 },
      [
        catalogKeys.itemDetail("episode-4"),
        itemKeys.detail("episode-4"),
        progressKeys.all,
        catalogKeys.itemDetail("series-1"),
        itemKeys.detail("series-1"),
        catalogKeys.seasonDetail("series-1", 2),
        catalogKeys.seasonEpisodes("series-1", 2),
        episodeKeys.seasonDetail("series-1", 2),
        episodeKeys.bySeason("series-1", 2),
        itemKeys.details(),
        episodeKeys.all,
      ],
    ],
    [
      "returns the expected related queries for a season target",
      { content_id: "season-2", type: "season", series_id: "series-1", season_number: 2 },
      [
        catalogKeys.itemDetail("season-2"),
        itemKeys.detail("season-2"),
        progressKeys.all,
        catalogKeys.itemDetail("series-1"),
        itemKeys.detail("series-1"),
        catalogKeys.seriesSeasons("series-1"),
        episodeKeys.seasons("series-1"),
        catalogKeys.seasonDetail("series-1", 2),
        catalogKeys.seasonEpisodes("series-1", 2),
        episodeKeys.seasonDetail("series-1", 2),
        episodeKeys.bySeason("series-1", 2),
        itemKeys.details(),
        episodeKeys.all,
        catalogKeys.itemEpisodes("season-2"),
        episodeKeys.byItem("season-2"),
      ],
    ],
    [
      "includes the ebook reader progress query for an ebook target",
      { content_id: "ebook-1", type: "ebook" },
      [
        catalogKeys.itemDetail("ebook-1"),
        itemKeys.detail("ebook-1"),
        progressKeys.all,
        ebookKeys.readerProgress("ebook-1"),
      ],
    ],
    [
      "returns the expected related queries for a series target",
      { content_id: "series-1", type: "series" },
      [
        catalogKeys.itemDetail("series-1"),
        itemKeys.detail("series-1"),
        progressKeys.all,
        catalogKeys.seriesSeasons("series-1"),
        episodeKeys.seasons("series-1"),
        itemKeys.details(),
        episodeKeys.all,
      ],
    ],
  ] satisfies Array<[string, Partial<ItemDetail>, unknown[]]>)("%s", (_name, item, keys) => {
    expect(getWatchedInvalidationKeys(makeItem(item))).toEqual(keys);
  });
});

describe("getCachedWatchedInvalidationKeys", () => {
  it("expands a series mutation to cached season detail and episode queries", () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(catalogKeys.seriesSeasons("series-1"), {
      seasons: [
        {
          content_id: "season-1",
          season_number: 1,
          is_specials: false,
          title: "Season 1",
          overview: "",
          air_date: null,
          episode_count: 2,
          poster_url: "",
          poster_thumbhash: "",
        },
      ],
    });
    queryClient.setQueryData(catalogKeys.itemEpisodes("season-1"), {
      episodes: [
        {
          content_id: "episode-1",
          season_number: 1,
          episode_number: 1,
          title: "Episode 1",
          overview: "",
          air_date: null,
          runtime: 42,
          still_url: "",
          still_thumbhash: "",
          files: [],
        },
      ],
    });

    expect(
      getCachedWatchedInvalidationKeys(
        queryClient,
        makeItem({
          content_id: "series-1",
          type: "series",
        }),
      ),
    ).toEqual(
      expect.arrayContaining([
        catalogKeys.itemDetail("season-1"),
        itemKeys.detail("season-1"),
        catalogKeys.itemEpisodes("season-1"),
        episodeKeys.byItem("season-1"),
        catalogKeys.seasonDetail("series-1", 1),
        catalogKeys.seasonEpisodes("series-1", 1),
        episodeKeys.seasonDetail("series-1", 1),
        episodeKeys.bySeason("series-1", 1),
        catalogKeys.itemDetail("episode-1"),
        itemKeys.detail("episode-1"),
      ]),
    );
  });
});
