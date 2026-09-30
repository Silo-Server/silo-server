import { describe, expect, it } from "vitest";

import {
  groupRatingSourcesByPlugin,
  parseRatingSources,
  toggleRatingSource,
} from "./ratingSources";

describe("rating source setting", () => {
  it("parses a comma-separated list", () => {
    expect(parseRatingSources(" rt_critic, ,metacritic ")).toEqual(["rt_critic", "metacritic"]);
    expect(parseRatingSources(undefined)).toEqual([]);
  });

  it("turns one source on or off and keeps the rest", () => {
    expect(toggleRatingSource("", "rt_critic", true)).toBe("rt_critic");
    expect(toggleRatingSource("kinopoisk,rt_critic", "rt_audience", true)).toBe(
      "kinopoisk,rt_critic,rt_audience",
    );
    expect(toggleRatingSource("kinopoisk,rt_critic", "rt_critic", false)).toBe("kinopoisk");
    expect(toggleRatingSource("rt_critic", "rt_critic", true)).toBe("rt_critic");
  });

  it("groups the ratings plugins declare by plugin and leaves out IMDb and TMDB", () => {
    const groups = groupRatingSourcesByPlugin([
      { source: "imdb", label: "IMDb", always_shown: true },
      { source: "tmdb", label: "TMDB", always_shown: true },
      {
        source: "rt_critic",
        label: "Rotten Tomatoes critics",
        always_shown: false,
        provider: "MDBList",
      },
      { source: "kinopoisk", label: "Kinopoisk", always_shown: false, provider: "Kinopoisk" },
      { source: "mdblist", label: "MDBList score", always_shown: false, provider: "MDBList" },
    ]);

    expect(groups.map((g) => [g.provider, g.sources.map((s) => s.source)])).toEqual([
      ["MDBList", ["rt_critic", "mdblist"]],
      ["Kinopoisk", ["kinopoisk"]],
    ]);
  });
});
