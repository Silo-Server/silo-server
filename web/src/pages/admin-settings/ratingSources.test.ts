// @vitest-environment node

import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import {
  EXTRA_RATING_SOURCES_KEY,
  RATING_POLICY_SETTLE_MS,
  saveAndRefreshRatings,
  groupRatingSourcesByPlugin,
  parseRatingSources,
  toggleRatingSource,
  undeclaredRatingSources,
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

  it("lists turned-on sources that no plugin declares", () => {
    const sources = [
      { source: "imdb", label: "IMDb", always_shown: true },
      { source: "rt_critic", label: "RT", always_shown: false, provider: "MDBList" },
    ];
    expect(
      undeclaredRatingSources(["rt_critic", "kinopoisk", "imdb", "kinopoisk"], sources),
    ).toEqual(["kinopoisk"]);
  });

  it("refreshes rating surfaces now and after the server cache expires when ratings change", async () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const scheduled: number[] = [];
    const save = vi.fn(async () => {});

    await saveAndRefreshRatings(
      { isDirty: (key) => key === EXTRA_RATING_SOURCES_KEY, save },
      queryClient,
      (run, ms) => {
        scheduled.push(ms);
        run();
      },
    );

    expect(save).toHaveBeenCalledOnce();
    expect(scheduled).toEqual([RATING_POLICY_SETTLE_MS]);
    // Rating surfaces and the admin source list, now and after the settle delay.
    expect(invalidate).toHaveBeenCalledTimes(4);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["admin", "ratingSources"] });
  });

  it("leaves cached surfaces alone when the rating choice did not change", async () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    await saveAndRefreshRatings({ isDirty: () => false, save: async () => {} }, queryClient, () => {
      throw new Error("nothing to schedule");
    });
    expect(invalidate).not.toHaveBeenCalled();
  });
});
