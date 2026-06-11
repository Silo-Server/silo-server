import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { catalogKeys, ebookKeys, episodeKeys, itemKeys, progressKeys } from "@/hooks/queries/keys";
import type { ItemDetail } from "@/api/types";
import {
  getCachedWatchedInvalidationKeys,
  getWatchedActionLabel,
  getWatchedInvalidationKeys,
  getWatchedToastMessage,
} from "./watchedState";

function makeItem(overrides: Partial<ItemDetail> = {}): ItemDetail {
  return {
    content_id: overrides.content_id ?? "item-1",
    type: overrides.type ?? "movie",
    title: overrides.title ?? "Title",
    original_title: overrides.original_title ?? "",
    year: overrides.year ?? 2024,
    overview: overrides.overview ?? "",
    tagline: overrides.tagline ?? "",
    runtime: overrides.runtime ?? 120,
    content_rating: overrides.content_rating ?? "",
    genres: overrides.genres ?? [],
    rating_imdb: overrides.rating_imdb ?? null,
    rating_tmdb: overrides.rating_tmdb ?? null,
    rating_rt_critic: overrides.rating_rt_critic ?? null,
    rating_rt_audience: overrides.rating_rt_audience ?? null,
    imdb_id: overrides.imdb_id ?? "",
    tmdb_id: overrides.tmdb_id ?? "",
    tvdb_id: overrides.tvdb_id ?? "",
    cast: overrides.cast ?? [],
    crew: overrides.crew ?? [],
    studios: overrides.studios ?? [],
    networks: overrides.networks ?? [],
    countries: overrides.countries ?? [],
    first_air_date: overrides.first_air_date ?? null,
    last_air_date: overrides.last_air_date ?? null,
    poster_url: overrides.poster_url ?? "",
    poster_thumbhash: overrides.poster_thumbhash ?? "",
    backdrop_url: overrides.backdrop_url ?? "",
    backdrop_thumbhash: overrides.backdrop_thumbhash ?? "",
    logo_url: overrides.logo_url ?? "",
    release_date: overrides.release_date ?? null,
    season_count: overrides.season_count ?? null,
    series_id: overrides.series_id,
    series_title: overrides.series_title,
    season_number: overrides.season_number,
    episode_number: overrides.episode_number,
    episode_count: overrides.episode_count ?? null,
    air_date: overrides.air_date ?? null,
    is_specials: overrides.is_specials ?? false,
    user_data: overrides.user_data,
    versions: overrides.versions ?? [],
    subtitles: overrides.subtitles ?? [],
    intro: overrides.intro ?? null,
    credits: overrides.credits ?? null,
  };
}

describe("getWatchedActionLabel", () => {
  it("returns mark watched for an unwatched movie", () => {
    expect(
      getWatchedActionLabel(
        makeItem({
          type: "movie",
          user_data: { played: false },
        }),
      ),
    ).toBe("Mark Watched");
  });

  it("returns mark season unwatched for a played season", () => {
    expect(
      getWatchedActionLabel(
        makeItem({
          type: "season",
          user_data: { played: true },
        }),
      ),
    ).toBe("Mark Season Unwatched");
  });

  it("returns mark series watched for an unplayed series", () => {
    expect(
      getWatchedActionLabel(
        makeItem({
          type: "series",
          user_data: { played: false },
        }),
      ),
    ).toBe("Mark Series Watched");
  });

  it("returns listening labels for audiobooks", () => {
    expect(
      getWatchedActionLabel(makeItem({ type: "audiobook", user_data: { played: false } })),
    ).toBe("Mark Listened");
    expect(
      getWatchedActionLabel(makeItem({ type: "audiobook", user_data: { played: true } })),
    ).toBe("Mark Unlistened");
  });

  it("returns reading labels for ebooks", () => {
    expect(getWatchedActionLabel(makeItem({ type: "ebook", user_data: { played: false } }))).toBe(
      "Mark Read",
    );
    expect(getWatchedActionLabel(makeItem({ type: "ebook", user_data: { played: true } }))).toBe(
      "Mark Unread",
    );
  });

  it("returns reading labels for manga series", () => {
    expect(getWatchedActionLabel(makeItem({ type: "manga", user_data: { played: false } }))).toBe(
      "Mark Read",
    );
    expect(getWatchedActionLabel(makeItem({ type: "manga", user_data: { played: true } }))).toBe(
      "Mark Unread",
    );
  });
});

describe("getWatchedToastMessage", () => {
  it("uses watched copy for video items", () => {
    expect(getWatchedToastMessage(makeItem({ type: "movie" }), true)).toBe("Marked as watched");
    expect(getWatchedToastMessage(makeItem({ type: "movie" }), false)).toBe("Marked as unwatched");
  });

  it("uses listened copy for audiobooks", () => {
    expect(getWatchedToastMessage(makeItem({ type: "audiobook" }), true)).toBe(
      "Marked as listened",
    );
    expect(getWatchedToastMessage(makeItem({ type: "audiobook" }), false)).toBe(
      "Marked as unlistened",
    );
  });

  it("uses read copy for ebooks", () => {
    expect(getWatchedToastMessage(makeItem({ type: "ebook" }), true)).toBe("Marked as read");
    expect(getWatchedToastMessage(makeItem({ type: "ebook" }), false)).toBe("Marked as unread");
  });

  it("uses read copy for manga", () => {
    expect(getWatchedToastMessage(makeItem({ type: "manga" }), true)).toBe("Marked as read");
    expect(getWatchedToastMessage(makeItem({ type: "manga" }), false)).toBe("Marked as unread");
  });
});

describe("getWatchedInvalidationKeys", () => {
  it("returns the expected related queries for an episode target", () => {
    expect(
      getWatchedInvalidationKeys(
        makeItem({
          content_id: "episode-4",
          type: "episode",
          series_id: "series-1",
          season_number: 2,
        }),
      ),
    ).toEqual([
      catalogKeys.itemDetail("episode-4"),
      itemKeys.detail("episode-4"),
      progressKeys.all,
      catalogKeys.itemDetail("series-1"),
      itemKeys.detail("series-1"),
      catalogKeys.seasonDetail("series-1", 2),
      catalogKeys.seasonEpisodes("series-1", 2),
      episodeKeys.seasonDetail("series-1", 2),
      episodeKeys.bySeason("series-1", 2),
      itemKeys.details(),
      episodeKeys.all,
    ]);
  });

  it("returns the expected related queries for a season target", () => {
    expect(
      getWatchedInvalidationKeys(
        makeItem({
          content_id: "season-2",
          type: "season",
          series_id: "series-1",
          season_number: 2,
        }),
      ),
    ).toEqual([
      catalogKeys.itemDetail("season-2"),
      itemKeys.detail("season-2"),
      progressKeys.all,
      catalogKeys.itemDetail("series-1"),
      itemKeys.detail("series-1"),
      catalogKeys.seriesSeasons("series-1"),
      episodeKeys.seasons("series-1"),
      catalogKeys.seasonDetail("series-1", 2),
      catalogKeys.seasonEpisodes("series-1", 2),
      episodeKeys.seasonDetail("series-1", 2),
      episodeKeys.bySeason("series-1", 2),
      itemKeys.details(),
      episodeKeys.all,
      catalogKeys.itemEpisodes("season-2"),
      episodeKeys.byItem("season-2"),
    ]);
  });

  it("includes the ebook reader progress query for an ebook target", () => {
    expect(
      getWatchedInvalidationKeys(
        makeItem({
          content_id: "ebook-1",
          type: "ebook",
        }),
      ),
    ).toEqual([
      catalogKeys.itemDetail("ebook-1"),
      itemKeys.detail("ebook-1"),
      progressKeys.all,
      ebookKeys.readerProgress("ebook-1"),
    ]);
  });

  it("returns the expected related queries for a series target", () => {
    expect(
      getWatchedInvalidationKeys(
        makeItem({
          content_id: "series-1",
          type: "series",
        }),
      ),
    ).toEqual([
      catalogKeys.itemDetail("series-1"),
      itemKeys.detail("series-1"),
      progressKeys.all,
      catalogKeys.seriesSeasons("series-1"),
      episodeKeys.seasons("series-1"),
      itemKeys.details(),
      episodeKeys.all,
    ]);
  });
});

describe("getCachedWatchedInvalidationKeys", () => {
  it("expands a series mutation to cached season detail and episode queries", () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(catalogKeys.seriesSeasons("series-1"), {
      seasons: [
        {
          content_id: "season-1",
          season_number: 1,
          is_specials: false,
          title: "Season 1",
          overview: "",
          air_date: null,
          episode_count: 2,
          poster_url: "",
          poster_thumbhash: "",
        },
      ],
    });
    queryClient.setQueryData(catalogKeys.itemEpisodes("season-1"), {
      episodes: [
        {
          content_id: "episode-1",
          season_number: 1,
          episode_number: 1,
          title: "Episode 1",
          overview: "",
          air_date: null,
          runtime: 42,
          still_url: "",
          still_thumbhash: "",
          files: [],
        },
      ],
    });

    expect(
      getCachedWatchedInvalidationKeys(
        queryClient,
        makeItem({
          content_id: "series-1",
          type: "series",
        }),
      ),
    ).toEqual(
      expect.arrayContaining([
        catalogKeys.itemDetail("season-1"),
        itemKeys.detail("season-1"),
        catalogKeys.itemEpisodes("season-1"),
        episodeKeys.byItem("season-1"),
        catalogKeys.seasonDetail("series-1", 1),
        catalogKeys.seasonEpisodes("series-1", 1),
        episodeKeys.seasonDetail("series-1", 1),
        episodeKeys.bySeason("series-1", 1),
        catalogKeys.itemDetail("episode-1"),
        itemKeys.detail("episode-1"),
      ]),
    );
  });
});
