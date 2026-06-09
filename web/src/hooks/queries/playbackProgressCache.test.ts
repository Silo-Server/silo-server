import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { ItemDetail, ProgressListResponse, WatchDetail } from "@/api/types";
import { catalogKeys, itemKeys, progressKeys } from "./keys";
import { applyPlaybackProgressToCache } from "./playbackProgressCache";

function makeItemDetail(): ItemDetail {
  return {
    content_id: "movie-1",
    type: "movie",
    title: "Example Movie",
    year: 2024,
    overview: "",
    tagline: "",
    runtime: 120,
    content_rating: "PG-13",
    genres: [],
    rating_imdb: null,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [],
    studios: [],
    networks: [],
    countries: [],
    first_air_date: null,
    last_air_date: null,
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    release_date: null,
    season_count: null,
    versions: [],
    subtitles: [],
    intro: null,
    credits: null,
    user_data: {
      played: false,
      is_in_progress: true,
      position_seconds: 120,
      duration_seconds: 3600,
      last_file_id: 11,
      last_resolution: "1080p",
      last_hdr: false,
      last_codec_video: "h264",
    },
  };
}

function makeWatchDetail(): WatchDetail {
  return {
    content_id: "movie-1",
    type: "movie",
    title: "Example Movie",
    overview: "",
    versions: [],
    subtitles: [],
    intro: null,
    credits: null,
    user_data: {
      played: false,
      is_in_progress: true,
      position_seconds: 120,
      duration_seconds: 3600,
      last_file_id: 11,
      last_resolution: "1080p",
      last_hdr: false,
      last_codec_video: "h264",
    },
  };
}

describe("applyPlaybackProgressToCache", () => {
  it("updates cached item detail, watch detail, and progress entries for the exited item", () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData(itemKeys.detail("movie-1"), makeItemDetail());
    queryClient.setQueryData(catalogKeys.itemDetail("movie-1"), makeItemDetail());
    queryClient.setQueryData(itemKeys.watchDetail("movie-1"), makeWatchDetail());
    queryClient.setQueryData<ProgressListResponse>(progressKeys.list("in_progress"), {
      progress: [
        {
          media_item_id: "movie-1",
          position_seconds: 120,
          duration_seconds: 3600,
          completed: false,
          updated_at: "2026-03-21T00:00:00.000Z",
        },
      ],
    });

    applyPlaybackProgressToCache(queryClient, {
      contentId: "movie-1",
      positionSeconds: 900,
      durationSeconds: 3600,
      lastFileId: 22,
      lastResolution: "4K",
      lastHDR: true,
      lastCodecVideo: "hevc",
    });

    const itemDetail = queryClient.getQueryData<ItemDetail>(itemKeys.detail("movie-1"));
    const catalogItemDetail = queryClient.getQueryData<ItemDetail>(
      catalogKeys.itemDetail("movie-1"),
    );
    const watchDetail = queryClient.getQueryData<WatchDetail>(itemKeys.watchDetail("movie-1"));
    const progressList = queryClient.getQueryData<ProgressListResponse>(
      progressKeys.list("in_progress"),
    );

    expect(itemDetail?.user_data).toMatchObject({
      played: false,
      is_in_progress: true,
      position_seconds: 900,
      duration_seconds: 3600,
      last_file_id: 22,
      last_resolution: "4K",
      last_hdr: true,
      last_codec_video: "hevc",
    });
    expect(catalogItemDetail?.user_data).toMatchObject({
      played: false,
      is_in_progress: true,
      position_seconds: 900,
      duration_seconds: 3600,
      last_file_id: 22,
      last_resolution: "4K",
      last_hdr: true,
      last_codec_video: "hevc",
    });
    expect(watchDetail?.user_data).toMatchObject({
      played: false,
      is_in_progress: true,
      position_seconds: 900,
      duration_seconds: 3600,
      last_file_id: 22,
      last_resolution: "4K",
      last_hdr: true,
      last_codec_video: "hevc",
    });
    expect(progressList?.progress).toEqual([
      expect.objectContaining({
        media_item_id: "movie-1",
        position_seconds: 900,
        duration_seconds: 3600,
      }),
    ]);
  });

  it("keeps the watched badge while a rewatch is in progress", () => {
    const queryClient = new QueryClient();

    queryClient.setQueryData<ItemDetail>(itemKeys.detail("movie-1"), {
      ...makeItemDetail(),
      user_data: {
        played: true,
        is_in_progress: false,
        position_seconds: 0,
        duration_seconds: 3600,
      },
    });

    applyPlaybackProgressToCache(queryClient, {
      contentId: "movie-1",
      positionSeconds: 300,
      durationSeconds: 3600,
    });

    // played is a one-way latch mirroring the server model: the rewatch gets
    // a live resume point without clearing watched state.
    expect(
      queryClient.getQueryData<ItemDetail>(itemKeys.detail("movie-1"))?.user_data,
    ).toMatchObject({
      played: true,
      is_in_progress: true,
      position_seconds: 300,
      duration_seconds: 3600,
    });
  });
});
