import { describe, expect, it } from "vitest";

import { getQuerySortOptions } from "./querySortOptions";

describe("getQuerySortOptions", () => {
  it("allows ebook book-native sorts without enabling narrator", () => {
    const fields = getQuerySortOptions({ relevanceScope: "ebook" }).map((option) => option.value);

    expect(fields).toContain("author");
    expect(fields).toContain("series");
    expect(fields).not.toContain("narrator");
  });
});
