import { describe, expect, it } from "vitest";
import type { LibraryCollection } from "@/api/types";
import { buildAllUserCollectionOptions } from "./useAllUserCollections";

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
});
