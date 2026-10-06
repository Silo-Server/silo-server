// @vitest-environment node

import type { MediaRequest, RequestMediaResult, RequestMediaSeason } from "@/api/types";
import { describe, expect, it } from "vitest";
import {
  canCancelOwnRequest,
  defaultRequestSeasons,
  flattenResultPages,
  formatRequestDisplayState,
  formatRequestSeasonMeta,
  formatSeasonList,
  formatSeasonProgress,
  latestRequestSeason,
  parseRequestMediaType,
  pendingPageSize,
  requestDetailHref,
  requestDiscoverSectionHref,
  requestDisplayState,
  requestSearchHref,
  requestSearchTypeForScope,
  upcomingRequestSeasons,
} from "./mediaRequests";

describe("flattenResultPages", () => {
  it("keeps page order and drops a title an earlier page already showed", () => {
    const title = (tmdbID: number, mediaType: "movie" | "series" = "movie") =>
      ({ media_type: mediaType, tmdb_id: tmdbID }) as RequestMediaResult;
    const pages = [
      { results: [title(1), title(2)] },
      { results: [title(2), title(3), title(1, "series")] },
    ];

    expect(flattenResultPages(pages).map((item) => `${item.media_type}-${item.tmdb_id}`)).toEqual([
      "movie-1",
      "movie-2",
      "movie-3",
      "series-1",
    ]);
    expect(flattenResultPages(undefined)).toEqual([]);
  });
});

describe("pendingPageSize", () => {
  it("expects a page the size of the last one, or TMDB's page size before any", () => {
    const results = (count: number) => Array.from({ length: count }) as RequestMediaResult[];
    expect(pendingPageSize([{ results: results(20) }, { results: results(17) }])).toBe(17);
    expect(pendingPageSize([{ results: [] }])).toBe(20);
    expect(pendingPageSize(undefined)).toBe(20);
  });
});

describe("requestSearchHref", () => {
  it("opens the app's search page on movies and series", () => {
    expect(requestSearchHref(" the bear ")).toBe("/catalog?source=query&q=the+bear&type=video");
  });

  it("keeps a movie or series type and drops anything else", () => {
    expect(requestSearchHref("dune", "movie")).toBe("/catalog?source=query&q=dune&type=movie");
    expect(requestSearchHref("dune", "series")).toBe("/catalog?source=query&q=dune&type=series");
    expect(requestSearchHref("dune", "all")).toBe("/catalog?source=query&q=dune&type=video");
  });
});

describe("requestSearchTypeForScope", () => {
  it("searches TMDB for what the catalog scope holds", () => {
    expect(requestSearchTypeForScope(undefined)).toBe("all");
    expect(requestSearchTypeForScope("video")).toBe("all");
    expect(requestSearchTypeForScope("movie")).toBe("movie");
    expect(requestSearchTypeForScope("series")).toBe("series");
    expect(requestSearchTypeForScope("episode")).toBe("series");
  });

  it("skips TMDB for books", () => {
    expect(requestSearchTypeForScope("audiobook")).toBeNull();
    expect(requestSearchTypeForScope("ebook")).toBeNull();
    expect(requestSearchTypeForScope("manga")).toBeNull();
  });
});

describe("requestDiscoverSectionHref", () => {
  it("builds a Discover row's page", () => {
    expect(requestDiscoverSectionHref("trending_movies")).toBe(
      "/requests/discover/trending_movies",
    );
  });
});

type CancelFields = Pick<MediaRequest, "status" | "outcome" | "targets">;

describe("canCancelOwnRequest", () => {
  const pending: CancelFields = { status: "pending", outcome: "active" };

  it("allows an active pending request", () => {
    expect(canCancelOwnRequest(pending)).toBe(true);
  });

  it("allows an approved request nothing has been sent for", () => {
    expect(canCancelOwnRequest({ ...pending, status: "approved", targets: [] })).toBe(true);
  });

  it.each<[string, CancelFields]>([
    [
      "approved and sent",
      {
        ...pending,
        status: "approved",
        targets: [{ quality: "1080p", status: "queued" }] as MediaRequest["targets"],
      },
    ],
    ["queued", { ...pending, status: "queued" }],
    ["downloading", { ...pending, status: "downloading" }],
    ["completed", { ...pending, status: "completed" }],
    ["already cancelled", { ...pending, outcome: "cancelled" }],
    ["declined", { ...pending, outcome: "declined" }],
  ])("refuses a request that is %s, as the server does", (_label, request) => {
    expect(canCancelOwnRequest(request)).toBe(false);
  });
});

describe("requestDisplayState", () => {
  it("prefers the state the server derived", () => {
    expect(requestDisplayState("completed", "active", "processing")).toBe("processing");
  });
});

describe("requestDetailHref", () => {
  it("builds the title detail route", () => {
    expect(requestDetailHref("movie", 603)).toBe("/title/movie/603");
    expect(requestDetailHref("series", 1399)).toBe("/title/series/1399");
  });
});

describe("parseRequestMediaType", () => {
  it.each([undefined, "", "tv", "Movie", "browse"])("rejects %j", (value) => {
    expect(parseRequestMediaType(value)).toBeUndefined();
  });
});

describe("season requests", () => {
  const now = new Date(Date.UTC(2026, 4, 24, 12));
  const season = (overrides: Partial<RequestMediaSeason>): RequestMediaSeason => ({
    season_number: 1,
    episode_count: 10,
    air_date: "2022-01-01",
    availability: "missing",
    requested: false,
    ...overrides,
  });

  it("names seasons compactly", () => {
    expect(formatSeasonList([2])).toBe("Season 2");
    expect(formatSeasonList([5, 1, 2, 3, 3])).toBe("Seasons 1–3, 5");
  });

  it("picks the aired seasons the library lacks and nobody requested", () => {
    const seasons = [
      season({ season_number: 1, availability: "available" }),
      season({ season_number: 2, availability: "partial" }),
      season({ season_number: 3, requested: true }),
      season({ season_number: 4 }),
      season({ season_number: 5, air_date: "2026-05-24" }),
      season({ season_number: 6, air_date: "2026-09-01" }),
      season({ season_number: 7, air_date: undefined, episode_count: 0 }),
    ];
    expect(defaultRequestSeasons(seasons, now)).toEqual([2, 4, 5]);
  });

  it("finds the latest season, skipping specials", () => {
    const aired = [
      season({ season_number: 0, air_date: "2025-01-01" }),
      season({ season_number: 1 }),
      season({ season_number: 2, air_date: "2025-06-01" }),
      season({ season_number: 3, air_date: "2026-09-01" }),
    ];
    expect(latestRequestSeason(aired, now)).toBe(2);
    // The newest aired season is already in the library: nothing to pick.
    aired[2] = season({ season_number: 2, availability: "available" });
    expect(latestRequestSeason(aired, now)).toBeNull();

    const unaired = [
      season({ season_number: 1, air_date: "2026-09-01" }),
      season({ season_number: 2, air_date: "2027-09-01" }),
    ];
    expect(latestRequestSeason(unaired, now)).toBe(1);
    expect(
      latestRequestSeason([season({ air_date: undefined, episode_count: 0 })], now),
    ).toBeNull();
  });

  it("lists the requestable seasons that haven't aired", () => {
    const seasons = [
      season({ season_number: 0, air_date: "2026-12-01" }),
      season({ season_number: 1 }),
      season({ season_number: 2, air_date: "2026-09-01" }),
      season({ season_number: 3, air_date: "2027-01-01", requested: true }),
      season({ season_number: 4, air_date: undefined, episode_count: 0 }),
    ];
    expect(upcomingRequestSeasons(seasons, now)).toEqual([2, 4]);
  });

  it("describes a season and a request's progress", () => {
    expect(formatRequestSeasonMeta(season({ episode_count: 1 }))).toBe("2022 · 1 episode");
    expect(formatRequestSeasonMeta(season({ air_date: undefined, episode_count: 0 }))).toBe(
      "Not announced",
    );
    expect(
      formatSeasonProgress([
        { season_number: 1, episodes_aired: 9, episodes_available: 9 },
        { season_number: 2, episodes_aired: 10, episodes_available: 4 },
      ]),
    ).toBe("13 of 19 episodes in the library");
    expect(
      formatSeasonProgress([{ season_number: 1, episodes_aired: 0, episodes_available: 3 }]),
    ).toBe("");
    expect(formatRequestDisplayState("partially_available")).toBe("Partially available");
  });
});
