import { describe, expect, it } from "vitest";

import { getQuerySortOptions, normalizeQuerySortForScope } from "./querySortOptions";

describe("query sort ebook scope", () => {
  it("includes book-native ebook sorts without narrator", () => {
    const values = getQuerySortOptions({ relevanceScope: "ebook" }).map((option) => option.value);

    expect(values).toContain("author");
    expect(values).toContain("series");
    expect(values).not.toContain("narrator");
  });

  it("normalizes narrator sort away for ebook scope", () => {
    const normalized = normalizeQuerySortForScope(
      { field: "narrator", order: "asc" },
      { relevanceScope: "ebook" },
    );

    expect(normalized.field).not.toBe("narrator");
    expect(normalized.field).toBe("title");
  });
});
