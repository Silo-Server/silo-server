import { describe, expect, it } from "vitest";

import type { LibraryTabCollection, ServerCollectionsLibrary } from "@/api/types";
import { serverCollectionLibraryIds } from "./useCollectionPageHeader";

function library(id: number, ids: string[], total = ids.length): ServerCollectionsLibrary {
  return {
    library_id: id,
    library_name: `Library ${id}`,
    total_count: total,
    collections: ids.map((cid) => ({ id: cid }) as LibraryTabCollection),
  };
}

describe("serverCollectionLibraryIds", () => {
  // Library 1 and 3 list only a teaser of their collections.
  const libraries = [library(1, ["a", "b"], 25), library(2, ["c"]), library(3, ["c", "d"], 30)];

  it.each([
    ["the library the page was opened in", "a", 3, libraries, [3]],
    ["the first library that lists it", "c", undefined, libraries, [2]],
    ["past every teaser: each library cut short", "zz", undefined, libraries, [1, 3]],
    ["no library lists it", "zz", undefined, [library(2, ["c"])], []],
    ["still loading the list", "a", undefined, undefined, undefined],
  ] as const)("%s", (_name, id, libraryId, list, want) => {
    expect(serverCollectionLibraryIds(id, libraryId, list)).toEqual(want);
  });
});
