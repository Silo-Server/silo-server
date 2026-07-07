import { describe, expect, it } from "vitest";
import type { FileVersion } from "@/api/types";
import {
  resolutionScore,
  audioScore,
  pickBestAttributes,
  RESOLUTION_RANK,
  AUDIO_RANK,
} from "./versionRankingUtils";

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

describe("RESOLUTION_RANK", () => {
  it("exports a record with expected keys", () => {
    expect(RESOLUTION_RANK["4k"]).toBe(4);
    expect(RESOLUTION_RANK["2160p"]).toBe(4);
    expect(RESOLUTION_RANK["1080p"]).toBe(2);
    expect(RESOLUTION_RANK["720p"]).toBe(1);
  });
});

describe("AUDIO_RANK", () => {
  it("exports a record with expected keys", () => {
    expect(AUDIO_RANK["atmos"]).toBe(6);
    expect(AUDIO_RANK["truehd"]).toBe(5);
    expect(AUDIO_RANK["aac"]).toBe(0);
  });
});

describe("resolutionScore", () => {
  it("ranks 4K highest", () => {
    expect(resolutionScore("4K")).toBe(4);
    expect(resolutionScore("2160p")).toBe(4);
  });

  it("ranks 1080p above 720p", () => {
    expect(resolutionScore("1080p")).toBeGreaterThan(resolutionScore("720p"));
  });

  it("is case-insensitive", () => {
    expect(resolutionScore("1080P")).toBe(resolutionScore("1080p"));
    expect(resolutionScore("4K")).toBe(resolutionScore("4k"));
  });

  it("returns -1 for unknown resolution", () => {
    expect(resolutionScore("8k")).toBe(-1);
    expect(resolutionScore("")).toBe(-1);
    expect(resolutionScore("unknown")).toBe(-1);
  });
});

describe("audioScore", () => {
  it("ranks atmos highest", () => {
    expect(audioScore("TrueHD Atmos")).toBeGreaterThan(audioScore("truehd"));
    expect(audioScore("Atmos")).toBeGreaterThan(audioScore("dts-hd"));
  });

  it("matches partial codec strings", () => {
    expect(audioScore("TrueHD Atmos")).toBe(AUDIO_RANK["atmos"]);
    expect(audioScore("DTS-HD Master Audio")).toBe(AUDIO_RANK["dts-hd"]);
    expect(audioScore("E-AC-3")).toBe(AUDIO_RANK["e-ac-3"]);
  });

  it("is case-insensitive", () => {
    expect(audioScore("ATMOS")).toBe(audioScore("atmos"));
    expect(audioScore("AAC")).toBe(audioScore("aac"));
  });

  it("returns -1 for unknown codec", () => {
    expect(audioScore("mp3")).toBe(-1);
    expect(audioScore("")).toBe(-1);
    expect(audioScore("pcm")).toBe(-1);
  });
});

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

  it("works with a single version", () => {
    const versions = [makeVersion({ resolution: "1080p", codec_audio: "dts", hdr: true })];
    const result = pickBestAttributes(versions);
    expect(result).toEqual({ resolution: "1080p", hdr: true, audioLabel: "DTS" });
  });
});
