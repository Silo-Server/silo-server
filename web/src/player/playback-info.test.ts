import { describe, expect, it } from "vitest";
import {
  buildPlaybackInfoSections,
  lowerQualityOption,
  qualityOptionsFromPlanV3,
  resolveActiveQualityOptionId,
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
  it("names Atmos for a source track whose title is the codec's long name", () => {
    const sourceAudio = (track: NonNullable<PlayerFileVersion["audio_tracks"]>[number]) =>
      rowValue(
        buildPlaybackInfoSections({
          streamUrl: "https://app.example.com/api/v1/stream/abc123",
          plan: fixturePlanV3({ delivery: "original_http" }),
          currentSourceVersion: makeVersion({ audio_tracks: [{ ...track, default: true }] }),
          runtimeStats: {
            playerWidth: 1920,
            playerHeight: 1080,
            videoWidth: 3840,
            videoHeight: 2160,
            droppedFrames: 0,
            corruptedFrames: 0,
          },
        }),
        "Current Source File",
        "Audio codec",
      );
    const longName = "ATSC A/52B (AC-3, E-AC-3)";
    expect(
      sourceAudio({ codec: "eac3", title: longName, profile: "Dolby Digital Plus + Dolby Atmos" }),
    ).toBe("DD+ Atmos");
    expect(sourceAudio({ codec: "eac3", title: longName })).toBe(longName);
    expect(
      sourceAudio({
        codec: "eac3",
        title: "Atmospheric score",
        profile: "Dolby Digital Plus + Dolby Atmos",
      }),
    ).toBe("DD+ Atmos");
  });

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
});

describe("qualityOptionsFromPlanV3", () => {
  it("renders the plan's fixed rungs verbatim with a locally prepended auto entry", () => {
    const options = qualityOptionsFromPlanV3(
      fixturePlanV3({
        available_qualities: [
          {
            label: "1080p-medium",
            display_name: "1080p Medium",
            height: 1080,
            bitrate_kbps: 6000,
            preserves_source: false,
          },
          { label: "480p", height: 480, bitrate_kbps: 1500, preserves_source: false },
        ],
      }),
    );

    expect(options.map((option) => option.id)).toEqual(["auto", "1080p-medium", "480p"]);
    expect(options[1]).toEqual({
      id: "1080p-medium",
      label: "1080p Medium",
      sublabel: "6 Mbps",
      resolution: "1080p",
      bitrateKbps: 6000,
    });
  });

  it("drops the source entry an older server still lists as original", () => {
    const options = qualityOptionsFromPlanV3(
      fixturePlanV3({
        available_qualities: [
          { label: "original", height: 2160, bitrate_kbps: 22_500, preserves_source: true },
          { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
        ],
      }),
    );

    expect(options.map((option) => option.id)).toEqual(["auto", "1080p-medium"]);
    expect(options.some((option) => option.label === "Original")).toBe(false);
  });

  it("offers no menu when the plan publishes no fixed rung", () => {
    // Audio-only plans and clients without HLS land here, on old servers with
    // a lone original entry and on new ones with none.
    for (const available_qualities of [
      [],
      [{ label: "original", bitrate_kbps: 128, preserves_source: true }],
    ]) {
      expect(qualityOptionsFromPlanV3(fixturePlanV3({ available_qualities }))).toEqual([]);
    }
  });
});

describe("resolveActiveQualityOptionId", () => {
  const options = qualityOptionsFromPlanV3(
    fixturePlanV3({
      available_qualities: [
        { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
        { label: "1080p-low", height: 1080, bitrate_kbps: 3000, preserves_source: false },
        { label: "720p-medium", height: 720, bitrate_kbps: 2000, preserves_source: false },
      ],
    }),
  );

  it("lights Auto for an auto preference even when the plan names an entry", () => {
    for (const preference of ["auto", "original", "Source", "max", ""]) {
      expect(resolveActiveQualityOptionId(options, preference, "1080p-medium")).toBe("auto");
    }
  });

  it("lights the entry the plan delivers for a fixed preference", () => {
    // A cap can land a plain 1080p preference on either rung; the plan says which.
    expect(resolveActiveQualityOptionId(options, "1080p", "1080p-low")).toBe("1080p-low");
    expect(resolveActiveQualityOptionId(options, "1080p", "720p-medium")).toBe("720p-medium");
  });

  it("lights nothing when the plan names no entry on the menu", () => {
    expect(resolveActiveQualityOptionId(options, "1080p")).toBeNull();
    expect(resolveActiveQualityOptionId(options, "1080p-medium", "540p")).toBeNull();
  });

  it("names no entry when there is no menu", () => {
    expect(resolveActiveQualityOptionId([], "auto")).toBeNull();
    expect(resolveActiveQualityOptionId([], "1080p", "1080p-medium")).toBeNull();
  });
});

describe("lowerQualityOption", () => {
  const options = qualityOptionsFromPlanV3(
    fixturePlanV3({
      available_qualities: [
        { label: "original", height: 2160, bitrate_kbps: 40_000, preserves_source: true },
        { label: "2160p-medium", height: 2160, bitrate_kbps: 20_000, preserves_source: false },
        { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
        { label: "720p", height: 720, bitrate_kbps: 3000, preserves_source: false },
      ],
    }),
  );

  it("steps down one rung from the lit entry", () => {
    expect(lowerQualityOption(options, "1080p", "1080p-medium", 6000)?.id).toBe("720p");
    expect(lowerQualityOption(options, "720p", "720p", 3000)).toBeNull();
  });

  it("steps down from the delivered bitrate when a cap encodes the lit rung lower", () => {
    expect(lowerQualityOption(options, "2160p-medium", "2160p-medium", 5000)?.id).toBe("720p");
  });

  it("steps down from the delivered bitrate when no rung is lit", () => {
    expect(lowerQualityOption(options, "auto", "1080p-medium", 6000)?.id).toBe("720p");
    expect(lowerQualityOption(options, "original", undefined, 40_000)?.id).toBe("2160p-medium");
    expect(lowerQualityOption(options, "1080p", undefined, 7000)?.id).toBe("1080p-medium");
    // A copy delivery reports no bitrate; the top transcode rung is lower.
    expect(lowerQualityOption(options, "auto")?.id).toBe("2160p-medium");
  });
});
