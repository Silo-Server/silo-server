import { describe, expect, it } from "vitest";
import type { LibraryCollection } from "@/api/types";
import { adminCollectionOptions } from "./useRowCollectionOptions";

function collection(id: string, overrides: Partial<LibraryCollection> = {}): LibraryCollection {
  return {
    id,
    title: `Collection ${id}`,
    library_id: 1,
    library_ids: [1],
    collection_type: "manual",
    visibility: "visible",
    item_count: 4,
    poster_url: `/${id}.jpg`,
    poster_thumbhash: `hash-${id}`,
    ...overrides,
  } as LibraryCollection;
}

describe("admin collection options", () => {
  const libraries = [
    { id: 1, name: "Movies" },
    { id: 2, name: "Kids" },
  ];

  it("offers visible library collections with their poster, count and type", () => {
    expect(adminCollectionOptions([collection("a")], libraries)).toEqual([
      {
        id: "a",
        title: "Collection a",
        source: "library",
        group: "Movies",
        library_id: 1,
        library_name: "Movies",
        collection_type: "manual",
        item_count: 4,
        poster_url: "/a.jpg",
        poster_thumbhash: "hash-a",
      },
    ]);
  });

  it("leaves out hidden collections, which a row could not show", () => {
    const options = adminCollectionOptions(
      [collection("shown"), collection("hidden", { visibility: "hidden" })],
      libraries,
    );
    expect(options.map((option) => option.id)).toEqual(["shown"]);
  });

  it("offers collections from every library, whatever the admin's own profile can open", () => {
    const options = adminCollectionOptions(
      [collection("a"), collection("b", { library_id: 2, library_ids: [2] })],
      libraries,
    );
    expect(options.map((option) => [option.id, option.group])).toEqual([
      ["a", "Movies"],
      ["b", "Kids"],
    ]);
  });
});
