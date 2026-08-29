import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";
import {
  catalogKeys,
  favoriteKeys,
  historyKeys,
  itemKeys,
  libraryCollectionKeys,
  mediaSurfaceKeys,
  personKeys,
  progressKeys,
  recKeys,
  sectionKeys,
  watchlistKeys,
} from "./keys";
import {
  cancelItemDetailQueries,
  invalidateMediaSurfaceQueries,
  removeItemFromHomeSectionCaches,
  scheduleMediaSurfaceInvalidation,
  setCachedItemDetail,
} from "./mediaSurfaceRefresh";

describe("invalidateMediaSurfaceQueries", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("cancels every detail cache shape, letting the default revert apply", async () => {
    const queryClient = new QueryClient();
    const cancel = vi.spyOn(queryClient, "cancelQueries").mockResolvedValue(undefined);

    await cancelItemDetailQueries(queryClient, "item-1");

    const filters = cancel.mock.calls[0]?.[0];
    expect(filters?.predicate?.({ queryKey: catalogKeys.itemDetail("item-1") } as never)).toBe(
      true,
    );
    expect(filters?.predicate?.({ queryKey: itemKeys.detail("item-1") } as never)).toBe(true);
    expect(filters?.predicate?.({ queryKey: catalogKeys.itemDetail("item-2") } as never)).toBe(
      false,
    );
    // `revert: false` would drop an in-flight detail query into an error state
    // carrying a CancelledError instead of quietly restoring it.
    expect(filters).not.toHaveProperty("revert");
  });

  it("marks item, section, progress, history, favorites, watchlist, and collection queries stale", async () => {
    const queryClient = new QueryClient();
    const browseKey = itemKeys.browse({
      q: "",
      type: "all",
      sort: "created_at",
      order: "desc",
      offset: 0,
      limit: 20,
    });

    queryClient.setQueryData(browseKey, { items: [] });
    queryClient.setQueryData(sectionKeys.homeItems("hero"), { section: { id: "hero" } });
    queryClient.setQueryData(sectionKeys.library(1), { sections: [] });
    queryClient.setQueryData(
      catalogKeys.list({
        source: "query",
        q: "star",
        limit: 20,
        offset: 0,
      }),
      { items: [] },
    );
    queryClient.setQueryData(recKeys.forYouMain(), { row: null });
    queryClient.setQueryData(recKeys.forYouRows(), { rows: [] });
    queryClient.setQueryData(personKeys.catalog("person-1", { limit: 24, offset: 0 }), {
      total: 0,
      has_more: false,
      items: [],
    });
    queryClient.setQueryData(progressKeys.list(), { progress: [] });
    queryClient.setQueryData(historyKeys.list(), { items: [] });
    queryClient.setQueryData(favoriteKeys.list(), { items: [] });
    queryClient.setQueryData(watchlistKeys.list(), { items: [] });
    queryClient.setQueryData(libraryCollectionKeys.items(7, "favorites"), { items: [] });

    await invalidateMediaSurfaceQueries(queryClient);

    expect(queryClient.getQueryState(browseKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(sectionKeys.homeItems("hero"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(sectionKeys.library(1))?.isInvalidated).toBe(true);
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
    expect(queryClient.getQueryState(recKeys.forYouMain())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(recKeys.forYouRows())?.isInvalidated).toBe(true);
    expect(
      queryClient.getQueryState(personKeys.catalog("person-1", { limit: 24, offset: 0 }))
        ?.isInvalidated,
    ).toBe(true);
    expect(queryClient.getQueryState(progressKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(historyKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(favoriteKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(watchlistKeys.list())?.isInvalidated).toBe(true);
    expect(
      queryClient.getQueryState(libraryCollectionKeys.items(7, "favorites"))?.isInvalidated,
    ).toBe(true);
  });

  it("marks the active catalog detail query stale for the mutated item", async () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(catalogKeys.itemDetail("item-1"), { content_id: "item-1" });

    await invalidateMediaSurfaceQueries(queryClient, { itemId: "item-1" });

    expect(queryClient.getQueryState(catalogKeys.itemDetail("item-1"))?.isInvalidated).toBe(true);
  });

  it("deduplicates overlapping surface matches into one invalidation pass", async () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    queryClient.setQueryData(catalogKeys.itemDetail("item-1"), { content_id: "item-1" });
    queryClient.setQueryData(favoriteKeys.check("item-1"), { is_favorite: true });

    await invalidateMediaSurfaceQueries(queryClient, { itemId: "item-1" });

    expect(invalidate).toHaveBeenCalledOnce();
    // No `cancelRefetch: false`: reusing an in-flight request would let a
    // response that predates the mutation satisfy the invalidation.
    expect(invalidate).toHaveBeenCalledWith({ predicate: expect.any(Function) });
  });

  it("preserves an optimistically updated item detail while refreshing derived surfaces", async () => {
    const queryClient = new QueryClient();
    const detailKey = catalogKeys.itemDetail("item-1");
    queryClient.setQueryData(detailKey, { content_id: "item-1", user_rating: 5 });

    await invalidateMediaSurfaceQueries(queryClient, {
      itemId: "item-1",
      skipItemDetail: true,
    });

    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(false);
  });

  it("does not refresh content-derived similar items after a user-state change", async () => {
    const queryClient = new QueryClient();
    const similarKey = recKeys.similar("item-1");
    const personalizedKey = recKeys.forYouMain();
    queryClient.setQueryData(similarKey, { items: [] });
    queryClient.setQueryData(personalizedKey, { row: null });

    await invalidateMediaSurfaceQueries(queryClient, {
      itemId: "item-1",
      skipSimilarItems: true,
    });

    expect(queryClient.getQueryState(similarKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(personalizedKey)?.isInvalidated).toBe(true);
  });

  it("refreshes similar items for content-changing invalidations", async () => {
    const queryClient = new QueryClient();
    const similarKey = recKeys.similar("item-1");
    queryClient.setQueryData(similarKey, { items: [] });

    await invalidateMediaSurfaceQueries(queryClient, { itemId: "item-1" });

    expect(queryClient.getQueryState(similarKey)?.isInvalidated).toBe(true);
  });

  it("coalesces rapid user-state refreshes outside the interaction frame", async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const watchedKey = ["episodes", "series-1", "season", 1] as const;
    queryClient.setQueryData(watchedKey, { episodes: [] });

    scheduleMediaSurfaceInvalidation(queryClient, {
      itemId: "item-1",
      watchedKeys: [watchedKey],
      skipItemDetail: true,
    });
    scheduleMediaSurfaceInvalidation(queryClient, {
      itemId: "item-1",
      skipItemDetail: true,
    });

    expect(invalidate).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(600);
    expect(invalidate).toHaveBeenCalledOnce();
    expect(queryClient.getQueryState(watchedKey)?.isInvalidated).toBe(true);
  });

  it("retains required detail and similar refreshes when coalescing with skip requests", async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    const detailKey = catalogKeys.itemDetail("item-1");
    const similarKey = recKeys.similar("item-1");
    queryClient.setQueryData(detailKey, { content_id: "item-1" });
    queryClient.setQueryData(similarKey, { items: [] });

    scheduleMediaSurfaceInvalidation(queryClient, {
      itemId: "item-1",
      skipItemDetail: true,
      skipSimilarItems: true,
    });
    scheduleMediaSurfaceInvalidation(queryClient, { itemId: "item-1" });

    await vi.advanceTimersByTimeAsync(600);
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(similarKey)?.isInvalidated).toBe(true);
  });

  it("bumps the home refresh signal only once the invalidation has landed", async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    const sectionKey = sectionKeys.homeItems("continue-watching");
    queryClient.setQueryData(sectionKey, { section: { id: "continue-watching" } });

    scheduleMediaSurfaceInvalidation(queryClient, { itemId: "item-1" });

    expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBeUndefined();
    await vi.advanceTimersByTimeAsync(600);

    // Home re-reads its sections through one-shot `fetchQuery` calls, so the
    // signal is only useful once the section caches are already invalidated.
    expect(queryClient.getQueryState(sectionKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(1);
  });

  it("bumps the home refresh signal when invalidation fails", async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    vi.spyOn(queryClient, "invalidateQueries").mockRejectedValue(new Error("refresh failed"));

    scheduleMediaSurfaceInvalidation(queryClient, { itemId: "item-1" });
    await vi.advanceTimersByTimeAsync(600);

    expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(1);
  });

  it("stops deferring once the coalescing window reaches its maximum wait", async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    // A stream that never pauses for the full debounce must still refresh.
    for (let elapsed = 0; elapsed < 4000; elapsed += 500) {
      scheduleMediaSurfaceInvalidation(queryClient, { itemId: "item-1" });
      await vi.advanceTimersByTimeAsync(500);
    }

    expect(invalidate).toHaveBeenCalled();
  });

  it("does not invalidate catalog list queries for a different library scope", async () => {
    const queryClient = new QueryClient();
    const moviesKey = catalogKeys.list({
      source: "section",
      scope: "library",
      section_id: "all",
      library_id: 1,
      limit: 60,
      offset: 0,
    });
    const internationalKey = catalogKeys.list({
      source: "section",
      scope: "library",
      section_id: "all",
      library_id: 3,
      limit: 60,
      offset: 0,
    });

    queryClient.setQueryData(moviesKey, { items: [] });
    queryClient.setQueryData(internationalKey, { items: [] });

    await invalidateMediaSurfaceQueries(queryClient, { libraryId: 3 });

    expect(queryClient.getQueryState(moviesKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(internationalKey)?.isInvalidated).toBe(true);
  });

  it("does not invalidate library section queries for a different library scope", async () => {
    const queryClient = new QueryClient();
    const moviesLayoutKey = sectionKeys.libraryLayout(1);
    const moviesSectionKey = sectionKeys.libraryItems(1, "recently-added");
    const internationalLayoutKey = sectionKeys.libraryLayout(3);

    queryClient.setQueryData(moviesLayoutKey, { sections: [] });
    queryClient.setQueryData(moviesSectionKey, { section: { id: "recently-added", items: [] } });
    queryClient.setQueryData(internationalLayoutKey, { sections: [] });

    await invalidateMediaSurfaceQueries(queryClient, { libraryId: 3 });

    expect(queryClient.getQueryState(moviesLayoutKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(moviesSectionKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(internationalLayoutKey)?.isInvalidated).toBe(true);
  });

  it("leaves home section queries alone for a library-scoped change", async () => {
    const queryClient = new QueryClient();
    const homeLayoutKey = sectionKeys.homeLayout();
    const homeItemsKey = sectionKeys.homeItems("recently-added");

    queryClient.setQueryData(homeLayoutKey, { sections: [] });
    queryClient.setQueryData(homeItemsKey, { section: { id: "recently-added", items: [] } });

    await invalidateMediaSurfaceQueries(queryClient, { libraryId: 3 });

    expect(queryClient.getQueryState(homeLayoutKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(homeItemsKey)?.isInvalidated).toBe(false);

    // An unscoped change — a favorite, a watch, a catalog import — still does.
    await invalidateMediaSurfaceQueries(queryClient);

    expect(queryClient.getQueryState(homeLayoutKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(homeItemsKey)?.isInvalidated).toBe(true);
  });

  it("does not invalidate collection queries for a different library scope", async () => {
    const queryClient = new QueryClient();
    const moviesKey = libraryCollectionKeys.list(1);
    const internationalKey = libraryCollectionKeys.items(3, "favorites");

    queryClient.setQueryData(moviesKey, { collections: [] });
    queryClient.setQueryData(internationalKey, { items: [] });

    await invalidateMediaSurfaceQueries(queryClient, { libraryId: 3 });

    expect(queryClient.getQueryState(moviesKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(internationalKey)?.isInvalidated).toBe(true);
  });

  it("sets all cached detail keys for the mutated item", () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(catalogKeys.itemDetail("item-1"), {
      content_id: "item-1",
      title: "Old",
    });
    queryClient.setQueryData(itemKeys.detail("item-1"), { content_id: "item-1", title: "Old" });

    setCachedItemDetail(queryClient, "item-1", {
      content_id: "item-1",
      title: "New",
    } as ItemDetail);

    expect(
      queryClient.getQueryData<{ title: string }>(catalogKeys.itemDetail("item-1"))?.title,
    ).toBe("New");
    expect(queryClient.getQueryData<{ title: string }>(itemKeys.detail("item-1"))?.title).toBe(
      "New",
    );
  });

  it("removes dismissed items from cached home section rows", () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(sectionKeys.homeItems("continue"), {
      section: {
        id: "continue",
        section_type: "continue_watching",
        total_count: 2,
        items: [{ content_id: "item-1" }, { content_id: "item-2" }],
      },
    });

    removeItemFromHomeSectionCaches(queryClient, "item-1", "continue_watching");

    type CachedHomeSection = {
      section: {
        total_count: number;
        items: Array<{ content_id: string }>;
      };
    };

    expect(queryClient.getQueryData<CachedHomeSection>(sectionKeys.homeItems("continue"))).toEqual({
      section: {
        id: "continue",
        section_type: "continue_watching",
        total_count: 1,
        items: [{ content_id: "item-2" }],
      },
    });
  });
});
