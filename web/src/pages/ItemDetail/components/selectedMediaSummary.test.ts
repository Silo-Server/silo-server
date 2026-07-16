import { describe, expect, it } from "vitest";
import type { FileVersion, PlaybackVariant } from "@/api/types";
import { resolveSelectedMediaSummary } from "./selectedMediaSummary";

function makeVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    resolution: overrides.resolution ?? "1080p",
    codec_video: overrides.codec_video ?? "h264",
    codec_audio: overrides.codec_audio ?? "aac",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "mkv",
    file_size: overrides.file_size ?? 0,
    duration: overrides.duration ?? 7200,
    bitrate: overrides.bitrate ?? 0,
    audio_tracks: overrides.audio_tracks,
    video_tracks: overrides.video_tracks,
  };
}

function makeVariant(
  versionsByPart: FileVersion[][],
  overrides: Partial<PlaybackVariant> = {},
): PlaybackVariant {
  return {
    variant_id: overrides.variant_id ?? "variant-1",
    part_count: overrides.part_count ?? versionsByPart.length,
    total_duration: overrides.total_duration,
    parts: versionsByPart.map((versions, index) => ({
      part_index: index + 1,
      versions,
    })),
  };
}

describe("resolveSelectedMediaSummary", () => {
  it("uses the selected file for duration and technical attributes", () => {
    const version = makeVersion({
      resolution: "1080p",
      codec_audio: "dts",
      hdr: false,
      duration: 11760,
    });

    expect(resolveSelectedMediaSummary(version, undefined, 163)).toEqual({
      durationMinutes: 196,
      resolution: "1080p",
      videoRangeLabel: "",
      audioLabel: "DTS",
    });
  });

  it("derives the video range label from the selected file's tracks", () => {
    const dolbyVision = makeVersion({
      hdr: true,
      video_tracks: [{ dolby_vision: "Profile 8.1", video_range_type: "DOVIWithHDR10" }],
    });

    expect(resolveSelectedMediaSummary(dolbyVision, undefined, 0).videoRangeLabel).toBe("DV HDR10");
    expect(
      resolveSelectedMediaSummary(makeVersion({ hdr: true }), undefined, 0).videoRangeLabel,
    ).toBe("HDR");
  });

  it("only considers audio tracks from the selected file", () => {
    const version = makeVersion({
      codec_audio: "aac",
      audio_tracks: [
        { language: "en", codec: "aac" },
        { language: "en", codec: "truehd" },
      ],
    });

    expect(resolveSelectedMediaSummary(version, undefined, 0).audioLabel).toBe("TrueHD");
  });

  it("uses the playback variant total for multipart editions", () => {
    const firstPart = makeVersion({ file_id: 1, duration: 3600 });
    const secondPart = makeVersion({ file_id: 2, duration: 4200 });
    const variant = makeVariant([[firstPart], [secondPart]], { total_duration: 7800 });

    expect(resolveSelectedMediaSummary(firstPart, [variant], 0).durationMinutes).toBe(130);
  });

  it("does not replace a single-part selected file duration with the variant maximum", () => {
    const selected = makeVersion({ file_id: 1, duration: 6000 });
    const longerAlternative = makeVersion({ file_id: 2, duration: 7200 });
    const variant = makeVariant([[selected, longerAlternative]], { total_duration: 7200 });

    expect(resolveSelectedMediaSummary(selected, [variant], 0).durationMinutes).toBe(100);
  });

  it("falls back to item runtime when no selected file duration is available", () => {
    expect(resolveSelectedMediaSummary(null, undefined, 42)).toEqual({
      durationMinutes: 42,
      resolution: "",
      videoRangeLabel: "",
      audioLabel: "",
    });
  });
});
