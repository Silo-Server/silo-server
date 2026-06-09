import { describe, expect, it } from "vitest";
import type { ItemDetail, Season } from "@/api/types";
import {
  formatSeasonMeta,
  getSeasonDisplayTitle,
  resolveLeafPrimaryAction,
  resolveEpisodeSiblingSeason,
  resolveSeriesPrimaryAction,
} from "./itemDetailLayout";

function makeSeason(overrides: Partial<Season> = {}): Season {
  return {
    content_id: overrides.content_id ?? "season-1",
    season_number: overrides.season_number ?? 1,
    is_specials: overrides.is_specials ?? false,
    title: overrides.title ?? "Season 1",
    overview: overrides.overview ?? "",
    air_date: overrides.air_date ?? null,
    episode_count: overrides.episode_count ?? 10,
    poster_url: overrides.poster_url ?? "",
    poster_thumbhash: overrides.poster_thumbhash ?? "",
    user_data: overrides.user_data,
  };
}

function makeEpisodeItem(overrides: Partial<ItemDetail> = {}): ItemDetail {
  return {
    content_id: overrides.content_id ?? "episode-1",
    type: "episode",
    title: overrides.title ?? "Episode 1",
    original_title: overrides.original_title ?? "",
    year: overrides.year ?? 2024,
    overview: overrides.overview ?? "",
    tagline: overrides.tagline ?? "",
    runtime: overrides.runtime ?? 42,
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
    series_id: "series_id" in overrides ? overrides.series_id : "series-1",
    series_title: "series_title" in overrides ? overrides.series_title : "Series 1",
    season_number: "season_number" in overrides ? overrides.season_number : 2,
    episode_number: "episode_number" in overrides ? overrides.episode_number : 5,
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

describe("resolveSeriesPrimaryAction", () => {
  it("returns resume when the active series episode is in progress", () => {
    const action = resolveSeriesPrimaryAction({
      seriesId: "series-1",
      seasons: [
        makeSeason({
          content_id: "season-1",
          user_data: {
            watched_count: 3,
            unplayed_count: 7,
            in_progress_count: 1,
            played: false,
          },
        }),
      ],
      continueWatching: [
        {
          contentId: "episode-4",
          seriesId: "series-1",
          title: "Episode 4",
        },
      ],
    });

    expect(action).toEqual({
      label: "Resume",
      directHref: "/watch/episode-4",
      context: "Continue Episode 4",
    });
  });

  it("returns play latest when the viewer is already working through the series", () => {
    const action = resolveSeriesPrimaryAction({
      seriesId: "series-1",
      seasons: [
        makeSeason({
          content_id: "season-1",
          season_number: 1,
          user_data: {
            watched_count: 10,
            unplayed_count: 0,
            in_progress_count: 0,
            played: true,
          },
        }),
        makeSeason({
          content_id: "season-2",
          season_number: 2,
          title: "Season 2",
          user_data: {
            watched_count: 2,
            unplayed_count: 8,
            in_progress_count: 0,
            played: false,
          },
        }),
      ],
      continueWatching: [],
    });

    expect(action).toEqual({
      label: "Play Latest",
      targetSeasonId: "season-2",
      targetEpisodeNumber: 3,
      context: "Jump back into Season 2",
    });
  });

  it("returns start from episode 1 when everything is unwatched", () => {
    const action = resolveSeriesPrimaryAction({
      seriesId: "series-1",
      seasons: [
        makeSeason({
          content_id: "season-1",
          season_number: 1,
          title: "Season 1",
          user_data: {
            watched_count: 0,
            unplayed_count: 10,
            in_progress_count: 0,
            played: false,
          },
        }),
        makeSeason({
          content_id: "season-2",
          season_number: 2,
          title: "Season 2",
          episode_count: 8,
          user_data: {
            watched_count: 0,
            unplayed_count: 8,
            in_progress_count: 0,
            played: false,
          },
        }),
      ],
      continueWatching: [],
    });

    expect(action).toEqual({
      label: "Start From Episode 1",
      targetSeasonId: "season-1",
      targetEpisodeNumber: 1,
      context: "Begin with Season 1",
    });
  });

  it("advances to the next season when the current one is already completed", () => {
    const action = resolveSeriesPrimaryAction({
      seriesId: "series-1",
      seasons: [
        makeSeason({
          content_id: "season-1",
          season_number: 1,
          title: "Season 1",
          user_data: {
            watched_count: 10,
            unplayed_count: 0,
            in_progress_count: 0,
            played: true,
          },
        }),
        makeSeason({
          content_id: "season-2",
          season_number: 2,
          title: "Season 2",
          episode_count: 8,
          user_data: {
            watched_count: 0,
            unplayed_count: 8,
            in_progress_count: 0,
            played: false,
          },
        }),
      ],
      continueWatching: [],
    });

    expect(action).toEqual({
      label: "Play Latest",
      targetSeasonId: "season-2",
      targetEpisodeNumber: 1,
      context: "Jump back into Season 2",
    });
  });
});

describe("resolveLeafPrimaryAction", () => {
  it("returns resume when a movie or episode is in progress", () => {
    expect(
      resolveLeafPrimaryAction(
        makeEpisodeItem({
          user_data: {
            played: false,
            is_in_progress: true,
            position_seconds: 600,
            duration_seconds: 2400,
          },
        }),
        "Play Episode",
      ),
    ).toEqual({
      label: "Resume",
      progress: 25,
    });
  });

  it("falls back to the default label when nothing has been started", () => {
    expect(
      resolveLeafPrimaryAction(
        makeEpisodeItem({
          user_data: {
            played: false,
            is_in_progress: false,
            position_seconds: 0,
            duration_seconds: 2400,
          },
        }),
        "Play Episode",
      ),
    ).toEqual({
      label: "Play Episode",
      progress: undefined,
    });
  });

  it("offers resume for a rewatch of a played item", () => {
    expect(
      resolveLeafPrimaryAction(
        makeEpisodeItem({
          user_data: {
            played: true,
            is_in_progress: true,
            position_seconds: 600,
            duration_seconds: 2400,
          },
        }),
        "Play Episode",
      ),
    ).toEqual({
      label: "Resume",
      progress: 25,
    });
  });
});

describe("formatSeasonMeta", () => {
  it("returns episode count regardless of user data", () => {
    expect(
      formatSeasonMeta(
        makeSeason({
          episode_count: 10,
          user_data: {
            watched_count: 6,
            unplayed_count: 4,
            in_progress_count: 0,
            played: false,
          },
        }),
      ),
    ).toBe("10 episodes");
  });

  it("returns episode count when user data is missing", () => {
    expect(formatSeasonMeta(makeSeason({ episode_count: 8, user_data: undefined }))).toBe(
      "8 episodes",
    );
  });
});

describe("getSeasonDisplayTitle", () => {
  it("formats specials correctly", () => {
    expect(
      getSeasonDisplayTitle(
        makeSeason({
          season_number: 0,
          is_specials: true,
          title: "",
        }),
      ),
    ).toBe("Specials");
  });
});

describe("resolveEpisodeSiblingSeason", () => {
  it("uses the episode series id and season number for sibling lookups", () => {
    expect(
      resolveEpisodeSiblingSeason(
        makeEpisodeItem({
          content_id: "episode-99",
          series_id: "series-42",
          season_number: 3,
        }),
      ),
    ).toEqual({
      seriesId: "series-42",
      seasonNumber: 3,
    });
  });

  it("returns null when the episode is missing season context", () => {
    expect(
      resolveEpisodeSiblingSeason(
        makeEpisodeItem({
          series_id: undefined,
          season_number: null,
        }),
      ),
    ).toBeNull();
  });
});
