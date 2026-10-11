import { describe, expect, it } from "vitest";

import {
  chartLockReason,
  chartOfSourceConfig,
  chartSource,
  chartSummary,
  franchiseSource,
  tmdbListSource,
  chartMediaKind,
  chartMediaTypes,
  chartName,
  defaultChart,
  normalizeChart,
  sameChart,
  TMDB_CHARTS,
} from "./tmdbSources";

describe("TMDB charts", () => {
  it("offers Both only for Trending, as the server's validator does", () => {
    for (const { preset } of TMDB_CHARTS) {
      expect(chartMediaTypes(preset).includes("all")).toBe(preset === "trending");
    }
  });

  it("locks Now playing and Upcoming to movies, Airing today and On the air to TV shows", () => {
    expect(chartMediaTypes("now_playing")).toEqual(["movie"]);
    expect(chartMediaTypes("upcoming")).toEqual(["movie"]);
    expect(chartMediaTypes("airing_today")).toEqual(["tv"]);
    expect(chartMediaTypes("on_the_air")).toEqual(["tv"]);
    expect(chartLockReason("upcoming")).toBe("TMDB has this chart for movies only");
    expect(chartLockReason("on_the_air")).toBe("TMDB has this chart for TV shows only");
    expect(chartLockReason("popular")).toBeNull();
  });

  it("keeps Trending over for Trending only and moves Show to one the chart offers", () => {
    expect(normalizeChart({ preset: "popular", mediaType: "all", timeWindow: "week" })).toEqual({
      preset: "popular",
      mediaType: "movie",
    });
    expect(normalizeChart({ preset: "airing_today", mediaType: "movie" })).toEqual({
      preset: "airing_today",
      mediaType: "tv",
    });
    expect(defaultChart("trending")).toEqual({
      preset: "trending",
      mediaType: "all",
      timeWindow: "day",
    });
  });

  it("names a chart and tells which titles it holds", () => {
    expect(chartName({ preset: "trending", mediaType: "movie", timeWindow: "week" })).toBe(
      "Trending Movies This Week",
    );
    expect(chartName({ preset: "trending", mediaType: "all", timeWindow: "day" })).toBe(
      "Trending Today",
    );
    expect(chartName({ preset: "top_rated", mediaType: "tv" })).toBe("Top Rated TV Shows");
    expect(chartMediaKind({ preset: "trending", mediaType: "all" })).toBe("mixed");
    expect(chartMediaKind({ preset: "upcoming", mediaType: "all" })).toBe("movie");
  });

  it("compares charts after normalizing them", () => {
    expect(
      sameChart(
        { preset: "popular", mediaType: "movie", timeWindow: "day" },
        {
          preset: "popular",
          mediaType: "movie",
        },
      ),
    ).toBe(true);
    expect(sameChart(defaultChart("trending"), { preset: "trending", mediaType: "all" })).toBe(
      true,
    );
    expect(
      sameChart(
        { preset: "trending", mediaType: "movie", timeWindow: "week" },
        {
          preset: "trending",
          mediaType: "movie",
          timeWindow: "day",
        },
      ),
    ).toBe(false);
  });
});

describe("stored TMDB sources", () => {
  it("reads a stored chart preset", () => {
    expect(
      chartOfSourceConfig({
        mode: "tmdb_preset",
        preset: "popular",
        media_type: "movie",
        limit: 35,
      }),
    ).toEqual({ preset: "popular", mediaType: "movie" });
    expect(chartOfSourceConfig({ preset: "trending", media_type: "all" })).toEqual({
      preset: "trending",
      mediaType: "all",
      timeWindow: "day",
    });
    expect(chartOfSourceConfig({ preset: "nope" })).toEqual(defaultChart("trending"));
  });

  it("builds a trending chart source with its window in the config and the URL", () => {
    expect(chartSource({ preset: "trending", mediaType: "all", timeWindow: "week" }, 50)).toEqual({
      source_url: "tmdb://trending/all/week",
      source_config: {
        mode: "tmdb_preset",
        preset: "trending",
        media_type: "all",
        time_window: "week",
        limit: 50,
      },
    });
  });

  it("builds a movie-only chart source without a window or limit", () => {
    expect(chartSource({ preset: "now_playing", mediaType: "movie", timeWindow: "day" })).toEqual({
      source_url: "tmdb://now_playing/movie",
      source_config: { mode: "tmdb_preset", preset: "now_playing", media_type: "movie" },
    });
  });

  it("stores a TMDB list link as the canonical list URL", () => {
    expect(tmdbListSource("themoviedb.org/list/8649937-marvel", 40)).toEqual({
      source_url: "https://www.themoviedb.org/list/8649937",
      source_config: {
        mode: "tmdb_list",
        url: "https://www.themoviedb.org/list/8649937",
        limit: 40,
      },
    });
  });

  it("builds a franchise source from a TMDB collection ID", () => {
    expect(franchiseSource(119)).toEqual({
      source_url: "tmdb://collection/119",
      source_config: { mode: "tmdb_collection", collection_id: 119 },
    });
  });

  it("says what a chart follows in words", () => {
    expect(chartSummary({ preset: "trending", mediaType: "movie", timeWindow: "week" })).toBe(
      "Trending movies, this week",
    );
    expect(chartSummary({ preset: "top_rated", mediaType: "tv" })).toBe("Top rated TV shows");
    expect(chartSummary({ preset: "trending", mediaType: "all", timeWindow: "day" })).toBe(
      "Trending movies and TV shows, today",
    );
  });
});
