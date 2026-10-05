import { describe, expect, it } from "vitest";

import {
  COLLECTION_FIELD_OPTIONS,
  COLLECTION_SORT_OPTIONS,
  getCollectionSortOptions,
} from "./collectionBuilderFields";

describe("collection rule fields and sorts", () => {
  it("includes the expanded collection builder field and sort lists", () => {
    expect(COLLECTION_FIELD_OPTIONS.map((field) => field.value)).toEqual(
      expect.arrayContaining(["rating_imdb", "watched", "favorited", "in_watchlist"]),
    );
    expect(COLLECTION_SORT_OPTIONS.map((sort) => sort.value)).toContain("rating_imdb");
  });

  it("includes last_air_date in the sort options", () => {
    expect(COLLECTION_SORT_OPTIONS.map((sort) => sort.value)).toContain("last_air_date");
  });

  it("shows personalized sort options only on user collection surfaces", () => {
    expect(getCollectionSortOptions(false).map((sort) => sort.value)).not.toContain("progress");
    expect(getCollectionSortOptions(true).map((sort) => sort.value)).toEqual(
      expect.arrayContaining(["progress", "date_viewed", "plays"]),
    );
  });
});
