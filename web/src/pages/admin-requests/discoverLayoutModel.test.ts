import { describe, expect, it } from "vitest";
import type { DiscoverLayout, DiscoverRow } from "@/api/v2/adminRequests";
import {
  NEW_ROW_TYPES,
  defaultTitle,
  describeDiscoverRow,
  missingStockRows,
  savedTitle,
  toHomeRow,
  withRecommendedDiscoverRows,
} from "./discoverLayoutModel";

const recommendedMovies: DiscoverRow = {
  id: "recommended_movies",
  type: "recommended",
  title: "Recommended movies for you",
  enabled: true,
  limit: 20,
  hide_available: true,
  recommended: { media_type: "movie" },
};

describe("recommended Discover rows", () => {
  it("keys a default recommended row's title, so it translates without a server key", () => {
    expect(toHomeRow(recommendedMovies).titleKey?.key).toBe(
      "home.section.discoverRecommendedMovies",
    );
    expect(toHomeRow({ ...recommendedMovies, title: "Picks" }).titleKey).toBeUndefined();
  });

  it("offers removed stock recommended rows via Bring back instead of New row", () => {
    expect(NEW_ROW_TYPES).not.toContain("recommended");
    const ids = missingStockRows([{ id: "recommended_series" }]).map((row) => row.id);
    expect(ids).toContain("recommended_movies");
    expect(ids).not.toContain("recommended_series");
  });

  it("falls back to the canonical recommended title when none is typed", () => {
    const emptySeries = {
      type: "recommended" as const,
      title: "",
      enabled: true,
      recommended: { media_type: "series" as const },
    };
    expect(defaultTitle(emptySeries)).toBe("Recommended series for you");
    expect(savedTitle(emptySeries)).toBe("Recommended series for you");
  });

  it("notes that the row is off only while the server serves no recommended rows", () => {
    const offParts = describeDiscoverRow(recommendedMovies, true);
    expect(offParts).toHaveLength(2);
    // Calm muted status rather than an amber row warning; the page banner carries the link.
    expect(offParts.every((part) => typeof part === "string")).toBe(true);
    // Its forced hide_available is not called out as a choice.
    expect(describeDiscoverRow(recommendedMovies, false)).toHaveLength(1);
    expect(describeDiscoverRow(recommendedMovies)).toHaveLength(1);
  });

  it("prepends stock recommended rows or re-enables disabled ones", () => {
    const trending: DiscoverRow = {
      id: "trending_movies",
      type: "tmdb_chart",
      title: "Trending Movies",
      enabled: true,
      limit: 20,
      chart: { preset: "trending", media_type: "movie", time_window: "week" },
    };
    const withoutRecs: DiscoverLayout = {
      customized: true,
      max_rows: 10,
      etag: '"1"',
      rows: [trending],
    };
    const added = withRecommendedDiscoverRows(withoutRecs);
    expect(added?.map((r) => r.id)).toEqual([
      "recommended_movies",
      "recommended_series",
      "trending_movies",
    ]);

    const disabledRec: DiscoverLayout = {
      customized: true,
      max_rows: 10,
      etag: '"2"',
      rows: [{ ...recommendedMovies, enabled: false }, trending],
    };
    const reenabled = withRecommendedDiscoverRows(disabledRec);
    expect(reenabled?.map((r) => [r.id, r.enabled])).toEqual([
      ["recommended_movies", true],
      ["trending_movies", true],
    ]);

    const full: DiscoverLayout = { ...withoutRecs, max_rows: 2 };
    expect(withRecommendedDiscoverRows(full)).toBeNull();
  });
});
