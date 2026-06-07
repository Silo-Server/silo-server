import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { ItemDetail } from "@/api/types";
import {
  catalogKeys,
  favoriteKeys,
  historyKeys,
  itemKeys,
  personKeys,
  progressKeys,
  recKeys,
  sectionKeys,
  watchlistKeys,
} from "./keys";
import {
  invalidateMediaSurfaceQueries,
  removeItemFromHomeSectionCaches,
  setCachedItemDetail,
} from "./mediaSurfaceRefresh";

describe("invalidateMediaSurfaceQueries", () => {
  it("marks item, section, progress, history, favorites, and watchlist queries stale", async () => {
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
  });

  it("marks the active catalog detail query stale for the mutated item", async () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(catalogKeys.itemDetail("item-1"), { content_id: "item-1" });

    await invalidateMediaSurfaceQueries(queryClient, { itemId: "item-1" });

    expect(queryClient.getQueryState(catalogKeys.itemDetail("item-1"))?.isInvalidated).toBe(true);
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

    expect(
      queryClient.getQueryData<CachedHomeSection>(sectionKeys.homeItems("continue")),
    ).toEqual({
      section: {
        id: "continue",
        section_type: "continue_watching",
        total_count: 1,
        items: [{ content_id: "item-2" }],
      },
    });
  });
});
