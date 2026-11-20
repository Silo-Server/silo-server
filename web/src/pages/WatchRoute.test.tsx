import { describe, expect, it } from "vitest";

import type { Profile, WatchDetail } from "@/api/types";

import {
  createWatchRouteRequest,
  buildWatchHref,
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

  it("builds a watch href that preserves the active playback query params", () => {
    expect(buildWatchHref(makeRequest("fileId=42&libraryId=7&restart=1"))).toBe(
      "/watch/movie-1?fileId=42&libraryId=7&restart=1",
    );
    expect(buildWatchHref(makeRequest())).toBe("/watch/movie-1");
  });

  it("treats an explicit pick as a different request from an Auto start", () => {
    // Two starts for the same row are not the same request: one may be
    // replaced by the server, the other may not.
    const explicit = createWatchRouteRequest({
      contentId: "movie-1",
      fileId: 42,
      explicitFileSelection: true,
    });
    const auto = createWatchRouteRequest({ contentId: "movie-1", fileId: 42 });

    expect(explicit.requestKey).not.toBe(auto.requestKey);
  });

  it("keeps a shuffle in the watch href and the request key", () => {
    const shuffled = makeRequest("shuffle=shuffle-1");

    expect(shuffled.shuffleId).toBe("shuffle-1");
    expect(buildWatchHref(shuffled)).toBe("/watch/movie-1?shuffle=shuffle-1");
    expect(shuffled.requestKey).not.toBe(makeRequest().requestKey);
  });
});

describe("buildWatchPageProps", () => {
  it("forwards a pick the navigation carried", () => {
    const props = buildWatchPageProps({
      request: createWatchRouteRequest({
        contentId: "movie-1",
        fileId: 42,
        explicitFileSelection: true,
      }),
      item: makeWatchDetail(),
      currentProfile: profile,
    });

    expect(props.explicitFileSelection).toBe(true);
  });

  it("leaves an Auto start substitutable", () => {
    const props = buildWatchPageProps({
      request: createWatchRouteRequest({ contentId: "movie-1", fileId: 42 }),
      item: makeWatchDetail(),
      currentProfile: profile,
    });

    expect(props.explicitFileSelection).toBe(false);
  });

  // A reload keeps the remembered row but loses the navigation that carried
  // why the row was chosen; the store's standing is what the watch page has
  // left to tell a pick from an Auto start.
  it("derives the pick from the remembered row after a reload", () => {
    const props = buildWatchPageProps({
      request: createWatchRouteRequest({ contentId: "movie-1", fileId: 42 }),
      item: makeWatchDetail(),
      currentProfile: profile,
      requestedFileId: 42,
      requestedVersionExplicit: true,
    });

    expect(props.explicitFileSelection).toBe(true);
  });

  it("does not pin the remembered row when the viewer did not name it", () => {
    const props = buildWatchPageProps({
      request: createWatchRouteRequest({ contentId: "movie-1", fileId: 42 }),
      item: makeWatchDetail(),
      currentProfile: profile,
      requestedFileId: 42,
      requestedVersionExplicit: false,
    });

    expect(props.explicitFileSelection).toBe(false);
  });

  it("does not pin a remembered row this request does not play", () => {
    const props = buildWatchPageProps({
      request: createWatchRouteRequest({ contentId: "movie-1", fileId: 42 }),
      item: makeWatchDetail(),
      currentProfile: profile,
      requestedFileId: 7,
      requestedVersionExplicit: true,
    });

    expect(props.explicitFileSelection).toBe(false);
  });

  it("forwards the watch detail's virtual ranking to the player", () => {
    const virtualRanking = {
      profile_label: "4K HDR",
      source: "profile" as const,
      criteria: [
        { attribute: "score", direction: "desc" as const },
        { attribute: "resolution", direction: "desc" as const },
      ],
    };
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail({ virtual_ranking: virtualRanking }),
      currentProfile: profile,
    });

    expect(props.virtualRanking).toEqual(virtualRanking);
  });

  it("leaves the virtual ranking absent for local content", () => {
    const props = buildWatchPageProps({
      request: makeRequest(),
      item: makeWatchDetail(),
      currentProfile: profile,
    });

    expect(props.virtualRanking).toBeUndefined();
  });

  it("keeps the empty playback variants identity stable across builds", () => {
    const build = () =>
      buildWatchPageProps({
        request: makeRequest(),
        item: makeWatchDetail(),
        currentProfile: profile,
      });

    expect(build().playbackVariants).toBe(build().playbackVariants);
  });

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

  it("prefers the device's requested row over the server's last-played substitute", () => {
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
        },
      }),
      currentProfile: profile,
      requestedFileId: 88,
    });

    expect(props).toMatchObject({
      resumeHints: {
        lastFileId: 88,
        lastResolution: "2160p",
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

  it.each([
    ["dvd_subtitle", { 42: 0 }],
    // Embedded PGS arrives as a sidecar the player draws, so a refused start
    // is not the subtitle's doing.
    ["pgs", {}],
  ] as const)(
    "passes a selected %s subtitle ordinal into the initial playback request",
    (codec, bitmapIndexes) => {
      const props = buildWatchPageProps({
        request: createWatchRouteRequest({
          contentId: "movie-1",
          fileId: 42,
          prePlaySubtitleMode: "explicit",
          prePlaySubtitleSelection: {
            source: "embedded",
            language: "en",
            codec,
            label: "English PGS",
            track_index: 4,
          },
        }),
        item: makeWatchDetail({
          versions: [
            {
              file_id: 42,
              resolution: "2160p",
              codec_video: "hevc",
              codec_audio: "truehd",
              hdr: true,
              container: "mkv",
              file_size: 1,
              duration: 120,
              bitrate: 25_000,
              effective_audio_track_index: 0,
              effective_audio_language: "en",
              subtitle_tracks: [
                {
                  index: 4,
                  language: "en",
                  codec,
                  title: "English PGS",
                },
              ],
            },
          ],
          subtitles: [
            {
              source: "embedded",
              language: "en",
              codec,
              forced: false,
              title: "English PGS",
            },
          ],
        }),
        currentProfile: profile,
      });

      // Bitmap burn-in rides the normal start request, while its separate marker
      // enables the subtitle-free fallback if that start is refused.
      expect(props.initialSubtitleTrackIndexByFileId).toEqual({ 42: 0 });
      expect(props.initialBitmapSubtitleTrackIndexByFileId).toEqual(bitmapIndexes);
    },
  );
});
