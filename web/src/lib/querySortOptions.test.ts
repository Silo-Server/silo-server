import { describe, expect, it } from "vitest";

import { getQuerySortOptions, normalizeQuerySortForScope } from "./querySortOptions";

describe("getQuerySortOptions", () => {
  it("offers a Rotten Tomatoes sort only when the server shows that source", () => {
    const fields = (shownRatingSources?: ReadonlySet<string>) =>
      getQuerySortOptions({ shownRatingSources }).map((option) => option.value);

    expect(fields(new Set(["imdb", "tmdb"]))).not.toContain("rating_rt_critic");
    expect(fields(new Set(["imdb", "tmdb"]))).toContain("rating_imdb");
    expect(fields(new Set(["imdb", "tmdb", "rt_audience"]))).toEqual(
      expect.arrayContaining(["rating_rt_audience"]),
    );
    expect(fields(new Set(["imdb", "tmdb", "rt_audience"]))).not.toContain("rating_rt_critic");
    expect(fields()).toContain("rating_rt_critic");
  });

  it("keeps a hidden rating sort that is already chosen", () => {
    const fields = getQuerySortOptions({
      shownRatingSources: new Set(["imdb", "tmdb"]),
      keepSortField: "rating_rt_critic",
    }).map((option) => option.value);

    expect(fields).toContain("rating_rt_critic");
    expect(fields).not.toContain("rating_rt_audience");
  });

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

it("limits History personalized ordering to its snapshot-backed Date Viewed sort", () => {
  const options = getQuerySortOptions({ includePersonalized: "date_viewed" });
  expect(options.filter((option) => option.personalized).map((option) => option.value)).toEqual([
    "date_viewed",
  ]);
  expect(
    normalizeQuerySortForScope(
      { field: "date_viewed", order: "asc" },
      { includePersonalized: "date_viewed" },
    ),
  ).toEqual({ field: "date_viewed", order: "asc" });
  expect(
    normalizeQuerySortForScope({ field: "progress" }, { includePersonalized: "date_viewed" }).field,
  ).not.toBe("progress");
});
