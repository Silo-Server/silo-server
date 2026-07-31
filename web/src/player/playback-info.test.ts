import { describe, expect, it } from "vitest";
import {
  buildPlaybackInfoSections,
  formatProtocol,
  formatStreamType,
  qualityOptionsFromPlanV3,
} from "./playback-info";
import { fixturePlanV3 } from "./protocol-v3.fixtures";
import type { PlayerFileVersion } from "./types";

function makeVersion(overrides: Partial<PlayerFileVersion> = {}): PlayerFileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    file_name: overrides.file_name ?? "Movie.2160p.mkv",
    resolution: overrides.resolution ?? "2160p",
    codec_video: overrides.codec_video ?? "hevc",
    codec_audio: overrides.codec_audio ?? "eac3",
    hdr: overrides.hdr ?? true,
    container: overrides.container ?? "mkv",
    file_size: overrides.file_size ?? 7.1 * 1024 ** 3,
    duration: overrides.duration ?? 7200,
    bitrate: overrides.bitrate ?? 22500,
    audio_channels: overrides.audio_channels ?? 6,
    video_tracks: overrides.video_tracks ?? [
      {
        codec: "hevc",
        profile: "Main 10",
        width: 3840,
        height: 2160,
        bitrate: 21900,
        video_range: "HDR10",
        color_range: "tv",
        dolby_vision: "Profile 8.1",
      },
    ],
    audio_tracks: overrides.audio_tracks ?? [
      {
        title: "EAC3 Dolby Digital Plus + Dolby Atmos",
        codec: "eac3",
        channels: 6,
        bitrate: 640,
        sample_rate: 48000,
        default: true,
      },
    ],
  };
}

function rowValue(
  sections: ReturnType<typeof buildPlaybackInfoSections>,
  sectionTitle: string,
  label: string,
): string {
  const section = sections.find((item) => item.title === sectionTitle);
  if (!section) {
    throw new Error(`section ${sectionTitle} not found`);
  }
  const row = section.rows.find((item) => item.label === label);
  if (!row) {
    throw new Error(`row ${label} not found`);
  }
  return row.value;
}

describe("playback info helpers", () => {
  it("formats a direct-play session with source metadata and live runtime stats", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "https://app.example.com/api/v1/stream/abc123",
      plan: fixturePlanV3({
        delivery: "original_http",
        stream: {
          url: "/stream/session-1/original",
          protocol: "http_progressive",
          mime_type: "video/x-matroska",
          headers: {},
          header_refresh: "none",
        },
        effective_recipe: { video_codec: "hevc", audio_codec: "eac3" },
      }),
      currentSourceVersion: makeVersion(),
      runtimeStats: {
        playerWidth: 2560,
        playerHeight: 1277,
        videoWidth: 3840,
        videoHeight: 2160,
        droppedFrames: 0,
        corruptedFrames: 0,
      },
    });

    expect(rowValue(sections, "Player", "Protocol")).toBe("https");
    expect(rowValue(sections, "Player", "Play method")).toBe("Direct Play");
    expect(rowValue(sections, "Player", "Stream type")).toBe("Progressive");
    expect(rowValue(sections, "Video Info", "Player dimensions")).toBe("2560x1277");
    expect(rowValue(sections, "Video Info", "Video resolution")).toBe("3840x2160");
    expect(rowValue(sections, "Playback Stream Info", "Video codec")).toBe("HEVC (direct)");
    expect(rowValue(sections, "Playback Stream Info", "Audio codec")).toBe("EAC3 (direct)");
    expect(rowValue(sections, "Current Source File", "Size")).toBe("7.1 GiB");
    expect(rowValue(sections, "Current Source File", "Bitrate")).toBe("22.5 Mbps");
    expect(rowValue(sections, "Current Source File", "Video codec")).toBe("HEVC Main 10");
    expect(rowValue(sections, "Current Source File", "Video range type")).toBe(
      "Dolby Vision Profile 8.1 (HDR10)",
    );
    expect(rowValue(sections, "Current Source File", "Color range")).toBe("Limited (tv)");
    expect(rowValue(sections, "Current Source File", "Audio codec")).toBe(
      "EAC3 Dolby Digital Plus + Dolby Atmos",
    );
    expect(rowValue(sections, "Current Source File", "Audio bitrate")).toBe("640 kbps");
    expect(rowValue(sections, "Current Source File", "Audio channels")).toBe("6");
    expect(rowValue(sections, "Current Source File", "Audio sample rate")).toBe("48,000 Hz");
  });

  it("reads copy-versus-transcode from the plan's transformations, not codec strings", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "https://app.example.com/api/v1/stream/remux/abc123",
      plan: fixturePlanV3({
        delivery: "server_remux_hls",
        effective_recipe: { video_codec: "h264", audio_codec: "aac" },
        transformations: [
          {
            name: "audio_to_aac",
            executor: "server",
            recipe_version: "v3.4",
            validated_claims: [],
          },
        ],
      }),
      currentSourceVersion: makeVersion({
        codec_video: "h264",
        codec_audio: "dts",
        hdr: false,
      }),
      runtimeStats: {},
    });

    expect(rowValue(sections, "Player", "Play method")).toBe("Direct Streaming");
    expect(rowValue(sections, "Player", "Stream type")).toBe("HLS");
    // The delivered video codec matches the source's, but the absence of a
    // `video_to_h264` transformation is what says it was copied.
    expect(rowValue(sections, "Playback Stream Info", "Video codec")).toBe("H.264 (copy)");
    expect(rowValue(sections, "Playback Stream Info", "Audio codec")).toBe("AAC (transcoded)");
  });

  it("labels a re-encoded stream as transcoded on both axes", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "https://app.example.com/api/v1/playback/transcode/session/master.m3u8",
      plan: fixturePlanV3({
        delivery: "server_transcode_hls",
        effective_recipe: { video_codec: "h264", audio_codec: "aac", height: 720 },
        transformations: [
          {
            name: "video_to_h264",
            executor: "server",
            recipe_version: "v3.4",
            validated_claims: [],
          },
          {
            name: "audio_to_aac",
            executor: "server",
            recipe_version: "v3.4",
            validated_claims: [],
          },
        ],
      }),
      currentSourceVersion: makeVersion({ hdr: false }),
      runtimeStats: {},
    });

    expect(rowValue(sections, "Player", "Play method")).toBe("Transcode");
    expect(rowValue(sections, "Playback Stream Info", "Video codec")).toBe("H.264 (transcoded)");
    expect(rowValue(sections, "Playback Stream Info", "Audio codec")).toBe("AAC (transcoded)");
  });

  it("shows explicit unavailable placeholders when metadata is missing", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "/api/v1/stream/test",
      plan: fixturePlanV3({ effective_recipe: {} }),
      currentSourceVersion: makeVersion({
        container: "",
        bitrate: 0,
        file_size: 0,
        video_tracks: [],
        audio_tracks: [],
        audio_channels: 0,
      }),
      runtimeStats: {},
    });

    expect(rowValue(sections, "Video Info", "Player dimensions")).toBe("—");
    expect(rowValue(sections, "Playback Stream Info", "Video codec")).toBe("—");
    expect(rowValue(sections, "Current Source File", "Container")).toBe("—");
    expect(rowValue(sections, "Current Source File", "Size")).toBe("—");
    expect(rowValue(sections, "Current Source File", "Audio sample rate")).toBe("—");
    expect(rowValue(sections, "Current Source File", "Color range")).toBe("—");
  });

  it("formats full and unspecified source color ranges", () => {
    const full = buildPlaybackInfoSections({
      streamUrl: "/api/v1/stream/full",
      plan: fixturePlanV3(),
      currentSourceVersion: makeVersion({ video_tracks: [{ color_range: "pc" }] }),
      runtimeStats: {},
    });
    const unknown = buildPlaybackInfoSections({
      streamUrl: "/api/v1/stream/unknown",
      plan: fixturePlanV3(),
      currentSourceVersion: makeVersion({ video_tracks: [{ color_range: "unknown" }] }),
      runtimeStats: {},
    });

    expect(rowValue(full, "Current Source File", "Color range")).toBe("Full (pc)");
    expect(rowValue(unknown, "Current Source File", "Color range")).toBe("Unknown");
  });

  it("shows the requested source when playback auto-switches to a lower version", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "https://app.example.com/api/v1/playback/transcode/session/master.m3u8",
      plan: fixturePlanV3({ requested_media_file_id: 1, effective_media_file_id: 2 }),
      currentSourceVersion: makeVersion({
        file_id: 2,
        resolution: "1080p",
        codec_video: "h264",
        hdr: false,
        file_name: "Movie.1080p.mkv",
        video_tracks: [{ codec: "h264", profile: "High", width: 1920, height: 1080 }],
      }),
      requestedVersion: makeVersion(),
      runtimeStats: {},
    });

    // The default fixture is a Dolby Vision (Profile 8.1) file, so the range
    // badge reads "DV" instead of the old generic boolean-derived "HDR".
    expect(rowValue(sections, "Player", "Auto-switched from")).toBe("2160p HEVC DV");
    expect(rowValue(sections, "Current Source File", "Video codec")).toBe("H.264 High");
  });

  it("names the transport from the plan's protocol", () => {
    expect(formatStreamType(fixturePlanV3())).toBe("HLS");
    expect(
      formatStreamType(
        fixturePlanV3({
          stream: {
            url: "/stream/session-1/original",
            protocol: "http_progressive",
            mime_type: "video/mp4",
            headers: {},
            header_refresh: "none",
          },
        }),
      ),
    ).toBe("Progressive");
    expect(formatProtocol("https://app.example.com/master.m3u8")).toBe("https");
  });
});

describe("qualityOptionsFromPlanV3", () => {
  it("renders the plan's ladder verbatim with a locally prepended auto entry", () => {
    const options = qualityOptionsFromPlanV3(
      fixturePlanV3({
        available_qualities: [
          { label: "original", height: 2160, bitrate_kbps: 22500, preserves_source: true },
          { label: "1080p", height: 1080, bitrate_kbps: 6000, preserves_source: false },
          { label: "480p", height: 480, bitrate_kbps: 1500, preserves_source: false },
        ],
      }),
    );

    expect(options.map((option) => option.id)).toEqual(["auto", "original", "1080p", "480p"]);
    expect(options[1]).toMatchObject({
      id: "original",
      label: "Original",
      sublabel: "22.5 Mbps",
      resolution: "2160p",
      bitrateKbps: 22500,
      isOriginal: true,
    });
    expect(options[2]).toMatchObject({
      id: "1080p",
      label: "1080p",
      sublabel: "6 Mbps",
      isOriginal: false,
    });
  });

  it("offers no auto entry when the plan publishes a single rung", () => {
    const options = qualityOptionsFromPlanV3(
      fixturePlanV3({
        available_qualities: [{ label: "original", bitrate_kbps: 128, preserves_source: true }],
      }),
    );

    // Audio-only plans and clients without HLS land here: one rung is not a
    // choice, but the array must stay non-empty so the version switcher renders.
    expect(options).toEqual([
      {
        id: "original",
        label: "Original",
        sublabel: "128 kbps",
        resolution: "",
        bitrateKbps: 128,
        isOriginal: true,
      },
    ]);
  });
});
