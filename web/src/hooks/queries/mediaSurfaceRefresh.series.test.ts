import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { catalogKeys } from "./keys";
import {
  invalidateMediaSurfaceQueries,
  relatedItemIds,
  scheduleMediaSurfaceInvalidation,
} from "./mediaSurfaceRefresh";

// Events that name the series of an episode the client hasn't cached (#796).
describe("media surface refresh for an event's series", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("refreshes the series an event names for an uncached episode", async () => {
    // An open series page caches its detail, seasons and one season's episodes.
    // Another client plays an episode outside that list (#796).
    const queryClient = new QueryClient();
    const seriesDetailKey = catalogKeys.itemDetail("series-1");
    const seasonsKey = catalogKeys.seriesSeasons("series-1");
    const seasonEpisodesKey = catalogKeys.seasonEpisodes("series-1", 1);
    const seasonDetailKey = catalogKeys.itemDetail("season-2");
    const shownEpisodeKey = catalogKeys.itemDetail("episode-1");
    const otherSeriesKey = catalogKeys.itemDetail("series-9");
    queryClient.setQueryData(seriesDetailKey, { content_id: "series-1", type: "series" });
    queryClient.setQueryData(seasonsKey, {
      seasons: [
        { content_id: "season-1", season_number: 1 },
        { content_id: "season-2", season_number: 2 },
      ],
    });
    queryClient.setQueryData(seasonEpisodesKey, { episodes: [{ content_id: "episode-1" }] });
    queryClient.setQueryData(seasonDetailKey, {
      content_id: "season-2",
      type: "season",
      series_id: "series-1",
      season_number: 2,
    });
    queryClient.setQueryData(shownEpisodeKey, { content_id: "episode-1", series_id: "series-1" });
    queryClient.setQueryData(otherSeriesKey, { content_id: "series-9", type: "series" });

    await invalidateMediaSurfaceQueries(queryClient, {
      itemId: "episode-elsewhere",
      seriesId: "series-1",
    });

    expect(queryClient.getQueryState(seriesDetailKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(seasonsKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(seasonEpisodesKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(seasonDetailKey)?.isInvalidated).toBe(true);
    // The series' other episodes don't change with this one.
    expect(queryClient.getQueryState(shownEpisodeKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(otherSeriesKey)?.isInvalidated).toBe(false);
  });

  it("keeps a coalesced event's series when a later one has none", async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    const seriesKey = catalogKeys.itemDetail("series-1");
    queryClient.setQueryData(seriesKey, { content_id: "series-1", type: "series" });

    scheduleMediaSurfaceInvalidation(queryClient, { itemId: "episode-x", seriesId: "series-1" });
    scheduleMediaSurfaceInvalidation(queryClient, { itemId: "episode-x" });
    await vi.advanceTimersByTimeAsync(700);

    expect(queryClient.getQueryState(seriesKey)?.isInvalidated).toBe(true);
  });

  it("adds the named series and its cached seasons to an item's related IDs", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(catalogKeys.seriesSeasons("series-1"), {
      seasons: [{ content_id: "season-1", season_number: 1 }],
    });

    expect(relatedItemIds(queryClient, "episode-elsewhere", "series-1")).toEqual(
      new Set(["episode-elsewhere", "series-1", "season-1"]),
    );
    expect(relatedItemIds(queryClient, "episode-elsewhere")).toEqual(
      new Set(["episode-elsewhere"]),
    );
  });
});
