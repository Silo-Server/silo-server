import { describe, expect, it } from "vitest";

import { getQuerySortOptions } from "./querySortOptions";

describe("getQuerySortOptions", () => {
  it("allows ebook book-native sorts without enabling narrator", () => {
    const fields = getQuerySortOptions({ relevanceScope: "ebook" }).map((option) => option.value);

    expect(fields).toContain("author");
    expect(fields).toContain("series");
    expect(fields).not.toContain("narrator");
  });

  it("uses reading labels for ebook personalized sorts", () => {
    const labelsByField = new Map(
      getQuerySortOptions({ includePersonalized: true, relevanceScope: "ebook" }).map((option) => [
        option.value,
        option.label,
      ]),
    );

    expect(labelsByField.get("date_viewed")).toBe("Date Read");
    expect(labelsByField.get("plays")).toBe("Reads");
  });

  it("keeps video labels for movie personalized sorts", () => {
    const labelsByField = new Map(
      getQuerySortOptions({ includePersonalized: true, relevanceScope: "movie" }).map((option) => [
        option.value,
        option.label,
      ]),
    );

    expect(labelsByField.get("date_viewed")).toBe("Date Viewed");
    expect(labelsByField.get("plays")).toBe("Plays");
  });
});
