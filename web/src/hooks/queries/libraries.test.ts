import { describe, expect, it } from "vitest";
import type { UserLibrary } from "@/api/types";
import { filterVisibleLibraries, normalizeLibraryIDs, parseLibraryIDList } from "./libraries";

const libraries: UserLibrary[] = [
  { id: 1, name: "Movies", type: "movies", sort_order: 0 },
  { id: 2, name: "Shows", type: "series", sort_order: 1 },
  { id: 3, name: "Anime", type: "series", sort_order: 2 },
];

describe("parseLibraryIDList", () => {
  it("returns an empty list when the setting is missing or invalid", () => {
    expect(parseLibraryIDList(null)).toEqual([]);
    expect(parseLibraryIDList(undefined)).toEqual([]);
    expect(parseLibraryIDList("")).toEqual([]);
    expect(parseLibraryIDList("nope")).toEqual([]);
    expect(parseLibraryIDList('{"ids":[1,2]}')).toEqual([]);
    expect(parseLibraryIDList({ ids: [1, 2] })).toEqual([]);
  });

  it("accepts the canonical array value directly", () => {
    expect(parseLibraryIDList([3, 1])).toEqual([3, 1]);
  });

  it("keeps only positive integer library ids", () => {
    expect(parseLibraryIDList([1, 2, 2, 0, -1, 3.5, "4"])).toEqual([1, 2]);
    expect(parseLibraryIDList('[1,2,2,0,-1,3.5,"4"]')).toEqual([1, 2]);
  });
});

describe("normalizeLibraryIDs", () => {
  it("produces a normalized id list", () => {
    expect(normalizeLibraryIDs([3, 1, 3, -1, 0])).toEqual([3, 1]);
  });
});

describe("filterVisibleLibraries", () => {
  it("returns all libraries when nothing is disabled", () => {
    expect(filterVisibleLibraries(libraries, [])).toEqual(libraries);
  });

  it("filters out disabled libraries", () => {
    expect(filterVisibleLibraries(libraries, [2, 99]).map((library) => library.id)).toEqual([1, 3]);
  });
});
