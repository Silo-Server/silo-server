import { describe, expect, it } from "vitest";
import { parseLibraryIDList } from "./libraries";

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
