import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { LibraryCollection } from "@/api/types";
import { buildAllUserCollectionOptions, useAllUserCollections } from "./useAllUserCollections";

const mocks = vi.hoisted(() => ({
  libraries: {} as Record<string, unknown>,
}));
vi.mock("./libraries", () => ({ useUserLibraries: () => mocks.libraries }));
vi.mock("./collections", () => ({
  useCollections: () => ({ data: [], isLoading: false, isFetching: false, isError: false }),
}));

function libraryCollection(id: string, title: string): LibraryCollection {
  return {
    id,
    title,
    collection_type: "smart",
    source_config: {},
    last_sync_status: "success",
  } as LibraryCollection;
}

describe("buildAllUserCollectionOptions", () => {
  it("keeps personal collections first and identifies their source", () => {
    const options = buildAllUserCollectionOptions(
      [{ id: 1, name: "Movies" }],
      [{ id: "personal", name: "Weekend Picks" }],
      [[libraryCollection("library", "Staff Picks")]],
    );

    expect(options.map(({ id, source, group }) => ({ id, source, group }))).toEqual([
      { id: "personal", source: "user", group: "My Collections" },
      { id: "library", source: "library", group: "Movies" },
    ]);
  });

  it("keeps the profile that made each personal collection", () => {
    const options = buildAllUserCollectionOptions(
      [],
      [{ id: "shared", name: "Road trips", creator_profile_id: "sibling" }],
      [],
    );

    expect(options[0]).toMatchObject({ id: "shared", creator_profile_id: "sibling" });
  });

  it("leaves the source config out of a library collection option", () => {
    const options = buildAllUserCollectionOptions([{ id: 1, name: "Movies" }], undefined, [
      [{ ...libraryCollection("library", "Staff Picks"), source_config: { mode: "x" } }],
    ]);

    expect(options).toEqual([
      {
        id: "library",
        title: "Staff Picks",
        source: "library",
        group: "Movies",
        library_id: 1,
        library_name: "Movies",
        collection_type: "smart",
        last_sync_status: "success",
      },
    ]);
  });

  it("lists a multi-library collection once with its combined scope", () => {
    const shared = libraryCollection("shared", "Network Originals");

    const options = buildAllUserCollectionOptions(
      [
        { id: 1, name: "Movies" },
        { id: 2, name: "TV Shows" },
      ],
      undefined,
      [[shared], [shared]],
    );

    expect(options).toHaveLength(1);
    expect(options[0]).toMatchObject({
      id: "shared",
      title: "Network Originals",
      source: "library",
      group: "Movies, TV Shows",
      library_name: "Movies, TV Shows",
    });
  });

  it("keeps separate same-titled collections distinct", () => {
    const options = buildAllUserCollectionOptions(
      [
        { id: 1, name: "Movies" },
        { id: 2, name: "TV Shows" },
      ],
      undefined,
      [
        [libraryCollection("movies", "Network Originals")],
        [libraryCollection("shows", "Network Originals")],
      ],
    );

    expect(options.map(({ id, group }) => ({ id, group }))).toEqual([
      { id: "movies", group: "Movies" },
      { id: "shows", group: "TV Shows" },
    ]);
  });

  it("carries the poster, title count and type of both sources for the row picker", () => {
    const options = buildAllUserCollectionOptions(
      [{ id: 1, name: "Movies" }],
      [
        {
          id: "personal",
          name: "Weekend Picks",
          collection_type: "mdblist",
          item_count: 12,
          poster_url: "/p.jpg",
          poster_thumbhash: "abc",
        },
      ],
      [
        [
          {
            ...libraryCollection("library", "Staff Picks"),
            collection_type: "manual",
            item_count: 23,
            poster_url: "/l.jpg",
            poster_thumbhash: "def",
          },
        ],
      ],
    );
    expect(
      options.map(({ id, collection_type, item_count, poster_url, poster_thumbhash }) => ({
        id,
        collection_type,
        item_count,
        poster_url,
        poster_thumbhash,
      })),
    ).toEqual([
      {
        id: "personal",
        collection_type: "mdblist",
        item_count: 12,
        poster_url: "/p.jpg",
        poster_thumbhash: "abc",
      },
      {
        id: "library",
        collection_type: "manual",
        item_count: 23,
        poster_url: "/l.jpg",
        poster_thumbhash: "def",
      },
    ]);
  });
});

describe("useAllUserCollections", () => {
  function render() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);
    return renderHook(() => useAllUserCollections(), { wrapper }).result.current;
  }

  it("is still loading while the libraries, and so their collections, are unknown", () => {
    mocks.libraries = { data: undefined, isLoading: true, isFetching: true, isError: false };
    expect(render()).toMatchObject({ collections: [], isLoading: true, isFetching: true });
  });

  it("has failed when the libraries didn't load", () => {
    mocks.libraries = { data: undefined, isLoading: false, isFetching: false, isError: true };
    expect(render()).toMatchObject({ isLoading: false, isError: true });
  });
});
