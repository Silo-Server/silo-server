import { describe, expect, it } from "vitest";

import { moveLibraryByOne } from "./adminLibraryOrder";

const ids = (libraries: { id: number }[] | null) => libraries?.map((library) => library.id) ?? null;

describe("moveLibraryByOne", () => {
  const libraries = [{ id: 10 }, { id: 20 }, { id: 30 }];

  it("swaps a library with its neighbour", () => {
    expect(ids(moveLibraryByOne(libraries, 20, -1))).toEqual([20, 10, 30]);
    expect(ids(moveLibraryByOne(libraries, 20, 1))).toEqual([10, 30, 20]);
    expect(ids(libraries)).toEqual([10, 20, 30]);
  });

  it("does nothing past either end or for an unknown library", () => {
    expect(moveLibraryByOne(libraries, 10, -1)).toBeNull();
    expect(moveLibraryByOne(libraries, 30, 1)).toBeNull();
    expect(moveLibraryByOne(libraries, 99, 1)).toBeNull();
  });
});
