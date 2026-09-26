import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { catalogKeys, historyKeys, personKeys, progressKeys, recKeys, sectionKeys } from "./keys";
import { invalidatePlaybackSurfaceQueries } from "./playbackSurfaceRefresh";

describe("invalidatePlaybackSurfaceQueries", () => {
  it("marks playback-derived queries as invalidated", async () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(progressKeys.list("in_progress"), { items: [] });
    queryClient.setQueryData(historyKeys.list(), { items: [] });
    queryClient.setQueryData(sectionKeys.homeItems("continue"), { section: { id: "continue" } });
    queryClient.setQueryData(sectionKeys.library(42), { sections: [] });
    queryClient.setQueryData(
      catalogKeys.list({
        source: "query",
        q: "star",
        limit: 20,
        offset: 0,
      }),
      { items: [] },
    );
    queryClient.setQueryData(recKeys.tasteProfile(), {
      top_genres: [],
      favorite_directors: [],
      signal_counts: {},
      updated_at: "2026-03-23T00:00:00.000Z",
    });
    queryClient.setQueryData(personKeys.catalog("person-1", { limit: 24, offset: 0 }), {
      total: 0,
      has_more: false,
      items: [],
    });
    queryClient.setQueryData(sectionKeys.homeLayout(), { sections: [] });

    await invalidatePlaybackSurfaceQueries(queryClient);

    expect(queryClient.getQueryState(progressKeys.list("in_progress"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(historyKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(sectionKeys.homeItems("continue"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(sectionKeys.library(42))?.isInvalidated).toBe(true);
    expect(
      queryClient.getQueryState(
        catalogKeys.list({
          source: "query",
          q: "star",
          limit: 20,
          offset: 0,
        }),
      )?.isInvalidated,
    ).toBe(true);
    expect(queryClient.getQueryState(recKeys.tasteProfile())?.isInvalidated).toBe(true);
    expect(
      queryClient.getQueryState(personKeys.catalog("person-1", { limit: 24, offset: 0 }))
        ?.isInvalidated,
    ).toBe(true);
    expect(queryClient.getQueryState(sectionKeys.homeLayout())?.isInvalidated).not.toBe(true);
  });
});
