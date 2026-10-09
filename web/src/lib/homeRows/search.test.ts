import { describe, expect, it } from "vitest";
import { pickerGroups } from "./catalog";
import { recipeCatalogFixture } from "./recipeCatalogFixture.test-support";
import { searchPickerGroups } from "./search";

const groups = pickerGroups(recipeCatalogFixture);

function types(query: string) {
  return searchPickerGroups(groups, query).flatMap((group) => group.cards.map((card) => card.type));
}

describe("picker search", () => {
  it("finds the 4K & HDR showcase for 4K", () => {
    expect(types("4K")).toEqual(["format_showcase"]);
  });

  it("finds the Popular cards for trending", () => {
    const groupsFound = searchPickerGroups(groups, "trending").map((group) => group.group);
    expect(groupsFound).toEqual(["popular"]);
    expect(types("trending")).toEqual(
      expect.arrayContaining(["trending_on_server", "trending_discover"]),
    );
  });

  it("finds rows by the words people use for them", () => {
    expect(types("Ghibli")).toEqual(["collection"]);
    expect(types("christmas")).toEqual(["seasonal_themed"]);
    expect(types("date night")).toEqual(["mood_collection"]);
  });

  it("returns every group for a blank query and none for a miss", () => {
    expect(searchPickerGroups(groups, "  ")).toBe(groups);
    expect(searchPickerGroups(groups, "zzzz-no-match")).toEqual([]);
  });
});
