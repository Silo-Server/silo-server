import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { catalogKeys, ratingKeys, recKeys, sectionKeys } from "./keys";
import { invalidateRatingSurfaceQueries } from "./ratingsSurfaceRefresh";

describe("invalidateRatingSurfaceQueries", () => {
  it("marks rating and recommendation-derived surfaces stale", async () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(ratingKeys.item("item-1"), {
      rating: 4,
      rated_at: "2026-03-23T00:00:00.000Z",
    });
    queryClient.setQueryData(recKeys.forYouMain(), { row: null });
    queryClient.setQueryData(recKeys.forYouRows(), { rows: [] });
    queryClient.setQueryData(recKeys.tasteProfile(), {
      top_genres: [],
      favorite_directors: [],
      signal_counts: {},
      updated_at: "2026-03-23T00:00:00.000Z",
    });
    queryClient.setQueryData(recKeys.similar("item-1"), { items: [] });
    queryClient.setQueryData(sectionKeys.homeItems("for-you"), { section: { id: "for-you" } });
    queryClient.setQueryData(catalogKeys.itemDetail("item-1"), {
      content_id: "item-1",
      user_rating: 4,
    });

    await invalidateRatingSurfaceQueries(queryClient, "item-1");

    expect(queryClient.getQueryState(ratingKeys.item("item-1"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(recKeys.forYouMain())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(recKeys.forYouRows())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(recKeys.tasteProfile())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(recKeys.similar("item-1"))?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(sectionKeys.homeItems("for-you"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(catalogKeys.itemDetail("item-1"))?.isInvalidated).toBe(false);
  });
});
