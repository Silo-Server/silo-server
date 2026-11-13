import { describe, expect, it } from "vitest";

import { getCollectionSortOptions } from "./collectionBuilderFields";

describe("collection rule fields and sorts", () => {
  it("shows personalized sort options only on user collection surfaces", () => {
    expect(getCollectionSortOptions(false).map((sort) => sort.value)).not.toContain("progress");
    expect(getCollectionSortOptions(true).map((sort) => sort.value)).toEqual(
      expect.arrayContaining(["progress", "date_viewed", "plays"]),
    );
  });
});
