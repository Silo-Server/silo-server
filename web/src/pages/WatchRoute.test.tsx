import { describe, expect, it } from "vitest";

import type { Profile, WatchDetail } from "@/api/types";

import {
  createWatchRouteRequest,
  buildWatchHref,
  buildWatchItemHref,
  buildWatchPageProps,
  buildWatchRouteRequest,
} from "./watchRouteHelpers";

const profile = {
  id: "profile-1",
  name: "Main",
  avatar: "avatar-1",
  has_pin: false,
  is_child: false,
  is_primary: true,
  max_content_rating: "pg-13",
  quality_preference: "auto",
  language: "en",
  subtitle_language: "fr",
  subtitle_mode: "off",
  show_forced_subtitles: false,
  auto_skip_intro: false,
  auto_skip_credits: false,
  library_restrictions_enabled: false,
  allowed_library_ids: null,
  max_playback_quality: "4k",
  created_at: "2026-03-23T00:00:00Z",
  updated_at: "2026-03-23T00:00:00Z",
} satisfies Profile;

function makeRequest(search = "") {
  return buildWatchRouteRequest("movie-1", new URLSearchParams(search));
}

function makeWatchDetail(overrides: Partial<WatchDetail> = {}): WatchDetail {
  return {
    content_id: "movie-1",
    type: "movie",
    title: "Spirited Away",
    overview: "Overview",
    versions: [],
    subtitles: [],
    intro: null,
    credits: null,
    ...overrides,
  };
}

describe("buildWatchRouteRequest", () => {
  it("parses file, library, and restart flags from the route search params", () => {
    expect(makeRequest("fileId=42&libraryId=7&restart=1")).toMatchObject({
      contentId: "movie-1",
      fileId: 42,
      libraryId: 7,
      restart: true,
    });
  });

  it("includes room credentials in the request key", () => {
    const first = makeRequest("room_id=room-1&room_token=token-a");
    const second = makeRequest("room_id=room-1&room_token=token-b");

    expect(first.requestKey).not.toBe(second.requestKey);
  });

  it("includes pre-play overrides in the request key", () => {
    const first = createWatchRouteRequest({
      contentId: "movie-1",
      fileId: 42,
      audioTrackIndex: 1,
      prePlaySubtitleMode: "auto",
    });
    const second = createWatchRouteRequest({
      contentId: "movie-1",
      fileId: 42,
      audioTrackIndex: 2,
      prePlaySubtitleMode: "auto",
    });

    expect(first.requestKey).not.toBe(second.requestKey);
  });

  it("builds an item-detail href that preserves the current library when present", () => {
    expect(buildWatchItemHref(makeRequest("libraryId=7"))).toBe("/item/movie-1?libraryId=7");
    expect(buildWatchItemHref(makeRequest())).toBe("/item/movie-1");
  });

  it("builds a watch href that preserves the active playback query params", () => {
    expect(buildWatchHref(makeRequest("fileId=42&libraryId=7&restart=1"))).toBe(
      "/watch/movie-1?fileId=42&libraryId=7&restart=1",
    );
    expect(buildWatchHref(makeRequest())).toBe("/watch/movie-1");
  });
});

describe("buildWatchPageProps", () => {
  it("prefers effective subtitle defaults from watch detail over raw profile values", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail({
        effective_subtitle_language: "en",
        effective_subtitle_mode: "always",
        effective_show_forced_subtitles: true,
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      preferredSubtitleLanguage: "en",
      subtitleMode: "always",
      showForcedSubtitles: true,
      profileLanguage: "en",
    });
  });

  it("falls back to the current profile subtitle defaults when watch detail has no override", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail({
        versions: [
          {
            file_id: 7,
            resolution: "1080p",
            codec_video: "h264",
            codec_audio: "aac",
            hdr: false,
            container: "mkv",
            file_size: 1,
            duration: 120,
            bitrate: 8000,
            effective_audio_language: "ja",
          },
        ],
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      preferredSubtitleLanguage: "fr",
      subtitleMode: "off",
      showForcedSubtitles: false,
      profileLanguage: "en",
      introSkipMode: "ask",
    });
    expect(props.versions[0]?.effective_audio_language).toBe("ja");
  });

  it("maps the legacy profile auto-skip preference onto the intro mode fallback", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail(),
      currentProfile: { ...profile, auto_skip_intro: true },
    });

    expect(props.introSkipMode).toBe("always");
  });

  it("passes an explicit empty subtitle override through to the player", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail({
        effective_subtitle_language: "",
        effective_subtitle_mode: "auto",
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      preferredSubtitleLanguage: "",
      subtitleMode: "auto",
      showForcedSubtitles: false,
      profileLanguage: "en",
    });
  });

  it("defaults showForcedSubtitles to true when both watch detail and profile omit it", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail(),
      currentProfile: { ...profile, show_forced_subtitles: undefined },
    });

    expect(props).toMatchObject({
      showForcedSubtitles: true,
    });
  });

  it("falls back to effective version hints when there is no item-specific resume", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail({
        effective_version_resolution: "1080p",
        effective_version_hdr: false,
        effective_version_codec_video: "h264",
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      resumeHints: {
        lastResolution: "1080p",
        lastHDR: false,
        lastCodecVideo: "h264",
      },
    });
  });

  it("prefers item-specific resume hints when they are available", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail({
        user_data: {
          played: false,
          is_in_progress: true,
          position_seconds: 120,
          duration_seconds: 3600,
          last_file_id: 77,
          last_resolution: "2160p",
          last_hdr: true,
          last_codec_video: "hevc",
        },
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      resumeHints: {
        lastFileId: 77,
        lastResolution: "2160p",
        lastHDR: true,
        lastCodecVideo: "hevc",
      },
    });
  });

  it("prefers item-specific resume hints over effective series version hints", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail({
        effective_version_resolution: "1080p",
        effective_version_hdr: false,
        effective_version_codec_video: "h264",
        user_data: {
          played: false,
          is_in_progress: true,
          position_seconds: 120,
          duration_seconds: 3600,
          last_file_id: 77,
          last_resolution: "2160p",
          last_hdr: true,
          last_codec_video: "hevc",
        },
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      resumeHints: {
        lastFileId: 77,
        lastResolution: "2160p",
        lastHDR: true,
        lastCodecVideo: "hevc",
      },
    });
  });

  it("forces a session-scoped restart when restart=1 is present", () => {
    const props = buildWatchPageProps({
      request: makeRequest("restart=1"),
      item: makeWatchDetail({
        user_data: {
          played: false,
          is_in_progress: true,
          position_seconds: 480,
          duration_seconds: 3600,
        },
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      initialPosition: 0,
      forceInitialPosition: true,
    });
  });

  it("forces room-driven playback starts to restart from room state", () => {
    const props = buildWatchPageProps({
      request: makeRequest("room_id=room-1&room_token=token-1&restart=1"),
      item: makeWatchDetail(),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      watchTogetherRoomId: "room-1",
      watchTogetherRoomToken: "token-1",
      forceInitialPosition: true,
      initialPosition: 0,
    });
  });

  it("overrides watch subtitle defaults when a pre-play subtitle choice is present", () => {
    const props = buildWatchPageProps({
      request: createWatchRouteRequest({
        contentId: "movie-1",
        prePlaySubtitleMode: "explicit",
        prePlaySubtitleSelection: {
          source: "downloaded",
          language: "en",
          codec: "srt",
          label: "English SDH",
          hearing_impaired: true,
        },
      }),
      item: makeWatchDetail({
        effective_subtitle_language: "fr",
        effective_subtitle_mode: "off",
      }),
      currentProfile: profile,
    });

    expect(props).toMatchObject({
      preferredSubtitleLanguage: "en",
      subtitleMode: "always",
      preferredSubtitleTrackSignature: {
        source: "downloaded",
        language: "en",
        codec: "srt",
        label: "English SDH",
        hearing_impaired: true,
      },
    });
  });
});
