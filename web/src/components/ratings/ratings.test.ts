import { describe, expect, it } from "vitest";

import { isOverlayOffered } from "@/lib/overlays/registry";
import { RATINGS_OVERLAYS } from "@/lib/overlays/registry/ratings";
import type { OverlayData } from "@/lib/overlays/types";

import { formatOutOfTen, formatPercent, primaryCardRating } from "./ratings";

// The server formats title-page ratings with ratingsources.Format; cards and
// badges format the same numbers here and must read the same.
describe("card rating formatting", () => {
  it("rounds halves away from zero, as the server does", () => {
    expect(formatOutOfTen(7.35)).toBe("7.4");
    expect(formatOutOfTen(7.05)).toBe("7.1");
    expect(formatOutOfTen(8.45)).toBe("8.5");
    expect(formatOutOfTen(8.25)).toBe("8.3");
    expect(formatOutOfTen(8)).toBe("8.0");
  });

  it("shows percentages as whole numbers", () => {
    expect(formatPercent(93)).toBe("93%");
    expect(formatPercent(0)).toBe("0%");
  });

  it("formats the hero rating and poster badges the same way", () => {
    expect(primaryCardRating({ rating_imdb: 7.35 })?.display).toBe("7.4");
    const imdbBadge = RATINGS_OVERLAYS.find((overlay) => overlay.id === "rating_imdb");
    expect(imdbBadge?.getValue({ rating_imdb: 7.35 } as OverlayData)).toBe("IMDb 7.4");
  });

  it("offers a Rotten Tomatoes badge only while the server shows that source", () => {
    const offered = (shown: string[]) =>
      RATINGS_OVERLAYS.filter((overlay) => isOverlayOffered(overlay, new Set(shown))).map(
        (overlay) => overlay.id,
      );

    expect(offered(["imdb", "tmdb"])).toEqual([
      "rating_imdb",
      "rating_tmdb",
      "content_rating",
      "advisory_age",
    ]);
    expect(offered(["imdb", "tmdb", "rt_audience"])).toContain("rating_rt_audience");
    expect(offered(["imdb", "tmdb", "rt_audience"])).not.toContain("rating_rt");
  });
});
