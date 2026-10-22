import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { catalogKeys, collectionKeys, libraryCollectionKeys } from "./keys";
import {
  invalidateLibraryCollectionQueries,
  invalidateUserCollectionQueries,
} from "./collectionSurfaceRefresh";

describe("invalidateUserCollectionQueries", () => {
  it("marks user collection lists and catalog-backed collection pages stale", async () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(collectionKeys.list(), []);
    queryClient.setQueryData(collectionKeys.items("collection-1"), []);
    queryClient.setQueryData(
      catalogKeys.list({
        source: "user_collection",
        collection_id: "collection-1",
        limit: 20,
        offset: 0,
      }),
      { items: [] },
    );

    await invalidateUserCollectionQueries(queryClient, "collection-1");

    expect(queryClient.getQueryState(collectionKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(collectionKeys.items("collection-1"))?.isInvalidated).toBe(
      true,
    );
    expect(
      queryClient.getQueryState(
        catalogKeys.list({
          source: "user_collection",
          collection_id: "collection-1",
          limit: 20,
          offset: 0,
        }),
      )?.isInvalidated,
    ).toBe(true);
  });
});

describe("invalidateLibraryCollectionQueries", () => {
  it("marks library collection lists and catalog-backed collection pages stale", async () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(libraryCollectionKeys.list(7), []);
    queryClient.setQueryData(collectionKeys.server(), []);
    queryClient.setQueryData(
      catalogKeys.list({
        source: "library_collection",
        collection_id: "collection-1",
        limit: 20,
        offset: 0,
      }),
      { items: [] },
    );

    await invalidateLibraryCollectionQueries(queryClient);

    expect(queryClient.getQueryState(libraryCollectionKeys.list(7))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(collectionKeys.server())?.isInvalidated).toBe(true);
    expect(
      queryClient.getQueryState(
        catalogKeys.list({
          source: "library_collection",
          collection_id: "collection-1",
          limit: 20,
          offset: 0,
        }),
      )?.isInvalidated,
    ).toBe(true);
  });
});
