import { describe, expect, it } from "vitest";

import { COLLECTION_FIELD_OPTIONS, getCollectionSortOptions } from "./collectionBuilderFields";

describe("collection rule fields and sorts", () => {
  it("includes the expanded collection builder field and sort lists", () => {
    expect(COLLECTION_FIELD_OPTIONS.map((field) => field.value)).toEqual(
      expect.arrayContaining(["rating_imdb", "watched", "favorited", "in_watchlist"]),
    );
  });

  it("shows personalized sort options only on user collection surfaces", () => {
    expect(getCollectionSortOptions(false).map((sort) => sort.value)).not.toContain("progress");
    expect(getCollectionSortOptions(true).map((sort) => sort.value)).toEqual(
      expect.arrayContaining(["progress", "date_viewed", "plays"]),
    );
  });
});
