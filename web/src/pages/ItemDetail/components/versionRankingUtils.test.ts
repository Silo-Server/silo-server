// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { FileVersion } from "@/api/types";
import { pickBestAttributes } from "./versionRankingUtils";

function makeVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    resolution: overrides.resolution ?? "1080p",
    codec_video: overrides.codec_video ?? "h264",
    codec_audio: overrides.codec_audio ?? "aac",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "mkv",
    file_size: overrides.file_size ?? 0,
    duration: overrides.duration ?? 0,
    bitrate: overrides.bitrate ?? 0,
    audio_tracks: overrides.audio_tracks,
    video_tracks: overrides.video_tracks,
    subtitle_tracks: overrides.subtitle_tracks,
  };
}

describe("pickBestAttributes", () => {
  it("returns null for empty versions array", () => {
    expect(pickBestAttributes([])).toBeNull();
  });

  it("picks the best resolution across versions", () => {
    const versions = [
      makeVersion({ resolution: "720p", codec_audio: "aac" }),
      makeVersion({ resolution: "1080p", codec_audio: "aac" }),
      makeVersion({ resolution: "4k", codec_audio: "aac" }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.resolution).toBe("4k");
  });

  it("picks the best audio across versions", () => {
    const versions = [
      makeVersion({ resolution: "1080p", codec_audio: "aac" }),
      makeVersion({ resolution: "1080p", codec_audio: "dts" }),
      makeVersion({ resolution: "1080p", codec_audio: "TrueHD Atmos" }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("Atmos");
  });

  it("sets hdr true when any version has hdr", () => {
    const versions = [
      makeVersion({ resolution: "1080p", hdr: false }),
      makeVersion({ resolution: "1080p", hdr: true }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.hdr).toBe(true);
  });

  it("sets hdr false when no version has hdr", () => {
    const versions = [
      makeVersion({ resolution: "1080p", hdr: false }),
      makeVersion({ resolution: "720p", hdr: false }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.hdr).toBe(false);
  });

  it("checks audio_tracks for higher-quality codecs", () => {
    const versions = [
      makeVersion({
        resolution: "1080p",
        codec_audio: "aac",
        audio_tracks: [{ codec: "TrueHD Atmos", language: "en" }],
      }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("Atmos");
  });

  it("ignores audio_tracks entries without codec", () => {
    const versions = [
      makeVersion({
        resolution: "1080p",
        codec_audio: "aac",
        audio_tracks: [{ language: "en" }],
      }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("AAC");
  });

  it("returns empty audioLabel when codec_audio is empty and no audio_tracks", () => {
    const versions = [makeVersion({ resolution: "1080p", codec_audio: "" })];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("");
  });
});
