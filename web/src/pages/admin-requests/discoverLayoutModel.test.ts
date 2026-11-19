import { describe, expect, it } from "vitest";
import type { DiscoverRow } from "@/api/v2/adminRequests";
import { describeDiscoverRow, missingStockRows, toHomeRow } from "./discoverLayoutModel";

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

  it("offers a removed default recommended row back", () => {
    const ids = missingStockRows([{ id: "recommended_series" }]).map((row) => row.id);
    expect(ids).toContain("recommended_movies");
    expect(ids).not.toContain("recommended_series");
  });

  it("warns that the row stays empty only while the server serves no recommended rows", () => {
    const hasWarning = (off: boolean) =>
      describeDiscoverRow(recommendedMovies, off).some((part) => typeof part === "object");
    expect(hasWarning(true)).toBe(true);
    // Its forced hide_available is not called out as a choice.
    expect(hasWarning(false)).toBe(false);
    expect(describeDiscoverRow(recommendedMovies)).toHaveLength(1);
  });
});
