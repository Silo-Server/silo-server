import { describe, expect, it } from "vitest";

import { parseRatingSources, toggleRatingSource } from "./ratingSources";

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
});
